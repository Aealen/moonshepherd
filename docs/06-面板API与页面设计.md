# 牧云 Herd · 面板 API 与页面设计

> 版本：v0.1（设计稿） · 日期：2026-10-25
>
> 姊妹文档：[01-产品总体设计](01-产品总体设计.md) · [03-迭代路线图](03-迭代路线图.md) · [04-通信协议规范](04-通信协议规范.md) · [05-安全设计](05-安全设计.md)
>
> 本文档是面板前端（Vue 3，见 [02](02-技术选型与工程结构.md) §1.2）与面板后端 HTTP 层（go-chi）的共同实现依据。Agent 侧 WSS 协议不在本文范围（见 04）。

## 1. API 总则

- **风格**：REST + JSON，统一前缀 `/api/v1`（破坏性变更升版本，与 04 的 proto_version 相互独立）
- **端口**：与 Agent WSS 网关同端口——浏览器走 `/api/v1/*`，Agent 走 `/agent`（见 05 §3.1 单端口收敛）
- **认证**：服务端会话，Cookie `HttpOnly + Secure + SameSite=Strict`（05 §3.2）；除 `POST /auth/login` 外全部端点要求已认证会话，未认证返回 `401`
- **方法语义**：一切状态变更仅 `POST/PUT/PATCH/DELETE`，`GET` 无副作用（CSRF 防线之一）
- **错误格式**（HTTP 状态码 + 机器可读 code）：

```json
{ "error": { "code": "node_not_found", "message": "node n_a1b2 not found" } }
```

常用 code：`unauthorized`（401）、`forbidden`（403）、`rate_limited`（429，附 `retry_after_s`）、`validation_failed`（400）、`*_not_found`（404）、`conflict`（409，如对非 draft 版本执行编辑）。

- **分页**：`?page=1&page_size=20`（page_size 上限 100）；列表响应统一：

```json
{ "data": [ ... ], "page": 1, "page_size": 20, "total": 57 }
```

- **时间**：一律 Unix 秒
- **敏感字段**：任何响应不返回 `credential_enc`、Token 哈希、会话 ID；注册 Token 明文仅在创建响应中返回**一次**

## 2. REST 端点清单（P0）

### 2.1 认证 auth

| 方法与路径 | 说明 | 请求/响应要点 |
|---|---|---|
| `POST /api/v1/auth/login` | 登录 | 请求 `{username, password}`；成功 Set-Cookie 会话并返回 `{expires_at}`；失败计入限流（429 时附 `retry_after_s`） |
| `POST /api/v1/auth/logout` | 登出 | 会话即焚 |
| `GET /api/v1/auth/me` | 当前会话 | `{username, role, session_expires_at}` |
| `PUT /api/v1/auth/password` | 改密码 | `{old_password, new_password}`（≥10 位）；成功后当前会话保持、其余会话失效 |
| P1 | `POST /auth/totp/setup`、`POST /auth/totp/verify` | TOTP 开通/校验 |

### 2.2 节点 nodes

| 方法与路径 | 说明 | 要点 |
|---|---|---|
| `GET /api/v1/nodes?status=&tag=&q=` | 节点列表 | 字段：id、name、os、agent_version、status(online/offline/pending)、tags、last_seen；响应头附统计 `X-Total-Online/Offline`（顶栏展示用） |
| `POST /api/v1/nodes/registration-tokens` | 发放注册 Token | 请求 `{note?, expires_hours?}`（默认 24）；响应 `{id, token: "rt_...(明文仅此一次)", expires_at}` |
| `GET /api/v1/nodes/registration-tokens` | Token 列表 | 不含明文；含状态 active/used/revoked |
| `DELETE /api/v1/nodes/registration-tokens/{id}` | 撤销 Token | |
| `GET /api/v1/nodes/{id}` | 节点详情 | 主机信息、capabilities、最近心跳快照、已用配置目标数、pending 指令数 |
| `PATCH /api/v1/nodes/{id}` | 改名/标签 | `{name?, tags?}` |
| `POST /api/v1/nodes/{id}/disable` | 禁用节点 | 立即断开其 WSS（close 4002）；禁用不删数据 |
| `POST /api/v1/nodes/{id}/enable` | 解禁 | |
| `DELETE /api/v1/nodes/{id}` | 删除节点 | **需确认参数 `?confirm=<节点名>`**（防误删）；级联删除凭据/应用清单/指令归属保留（审计不可删，node_id 置 tombstone 标记） |
| `POST /api/v1/nodes/{id}/probe` | 触发即时探测 | `{probe_names?: []}`；创建 `probe` 指令并返回 command_id |

### 2.3 应用 apps

| 方法与路径 | 说明 | 要点 |
|---|---|---|
| `GET /api/v1/apps?node_id=&probe=&app_type=&status=&q=` | 跨节点应用清单 | 应用中心主查询；行含 node_name 冗余便于展示 |
| `GET /api/v1/nodes/{id}/apps` | 单节点应用清单 | 节点详情"应用"tab 用 |

`detail` 为探测器原始 JSON，前端**纯文本渲染**（05 §3.6）。

### 2.4 指令 commands

| 方法与路径 | 说明 | 要点 |
|---|---|---|
| `POST /api/v1/nodes/{id}/commands` | 创建指令 | 请求 `{type: "exec"|"probe"|"config.apply", payload, timeout_s?}`；响应 `{command_id, status: "pending"}`；节点 offline 时 `exec` 返回 `409 conflict`（拒绝创建注定 pending 的交互式命令，`probe`/`config.apply` 允许排队） |
| `GET /api/v1/commands?node_id=&type=&status=&page=` | 指令历史 | 列表不含 stdout/stderr 正文（省流量） |
| `GET /api/v1/commands/{id}` | 指令详情 | 含 stdout/stderr/truncated、exit_code、stages[]（config.apply） |
| `POST /api/v1/commands/{id}/retry` | 人工重发 | 仅 `lost`/`failed` 可重发；**生成新 command_id**（04 §4.2） |

exec 创建示例：

```json
// POST /api/v1/nodes/n_a1b2/commands
{ "type": "exec", "payload": { "cmd": "df -h /" }, "timeout_s": 60 }
// 200
{ "command_id": "c_7d6e...", "status": "pending" }
```

### 2.5 配置管理 configs

| 方法与路径 | 说明 | 要点 |
|---|---|---|
| `GET /api/v1/nodes/{id}/configs` | 该节点配置目标列表 | 按探测发现的 `node_id × app_type × target_path` 分组；含当前生效版本、最近下发结果 |
| `POST /api/v1/nodes/{id}/configs` | 新建配置目标 + 首版 draft | `{app_type, target_path, content}`；app_type 必须已注册 validate/reload（01 §6.2） |
| `GET /api/v1/configs/{version_id}` | 版本详情 | content、checksum、status(draft/applied/rolled_back)、apply_result |
| `PUT /api/v1/configs/{version_id}` | 编辑 draft | 仅 draft 可编辑；每次保存产生新 draft 版本号（不可变版本链） |
| `POST /api/v1/configs/{version_id}/apply` | 下发 | 创建 `config.apply` 指令，返回 command_id；version 状态机 draft → applied |
| `POST /api/v1/configs/{version_id}/rollback` | 回滚 | 将该版本内容作为新 draft 再走 apply（01 §6.2 "回滚即下发"） |
| `GET /api/v1/configs/versions?node_id=&app_type=&target_path=&page=` | 版本历史 | 按目标路径的版本列表（时间线） |

### 2.6 审计与系统 system

| 方法与路径 | 说明 | 要点 |
|---|---|---|
| `GET /api/v1/audit-logs?user_id=&action=&page=` | 面板操作审计 | 只读、不可删除（05 §3.3） |
| `GET /api/v1/system/info` | 面板信息 | 版本、节点统计、库大小、运行时长 |
| P1 | `GET /system/sessions`、`DELETE /system/sessions/{id}` | 会话管理/强制下线 |

## 3. 浏览器实时事件通道（WebSocket）

浏览器与面板间单独一条 WS：`GET /api/v1/events`（同 Cookie 认证；与 Agent 的 `/agent` 网关完全隔离）。

订阅模型：连接后客户端发 `{"sub": {"node_ids": ["*"], "topics": ["node.status","command.status"]}}`；面板按订阅推送，避免全量广播。

| 事件 topic | 触发时机 | payload |
|---|---|---|
| `node.status` | 上下线/禁用/删除 | `{node_id, status}` |
| `node.snapshot` | 收到心跳 | `{node_id, cpu_percent, mem, disk, net, load1}`（详情页订阅单节点） |
| `command.status` | 指令状态机每次迁移 | `{command_id, node_id, status}` |
| `report.updated` | 探测上报完成 | `{node_id, probe_name}`（列表页提示"有更新"） |
| `node.registered` | 新节点注册成功 | `{node_id, name}`（Token 页面/通知） |

断线由前端自动重连（指数退避），重连后全量刷新当前页数据兜底（WS 只做增量提示，**不是数据唯一来源**——页面初次渲染一律走 REST，保证刷新可用）。

## 4. 前端页面结构（P0）

### 4.1 路由与布局

登录页独立成页；主布局 = 左侧固定导航 + 顶栏（在线/离线节点统计、当前用户、登出）。

| 路由 | 页面 | 主要内容 | P1 扩展位 |
|---|---|---|---|
| `/login` | 登录 | 用户名密码；限流时展示锁定剩余时间 | TOTP 输入 |
| `/nodes` | 节点列表 | 表格（名称/系统/Agent 版本/状态/标签/最后心跳）；「发放注册 Token」按钮（弹窗展示一次性 Token + install.sh 命令模板，可复制） | 批量标签 |
| `/nodes/:id` | 节点详情 | Tabs：**概览**（主机信息 + 实时资源快照卡片，WS 驱动）、**应用**（探测清单）、**指令**（该节点历史）、**配置**（配置目标列表 → 进入版本管理） | 监控历史曲线（ECharts）、docker 容器操作面板、日志查看 |
| `/apps` | 应用中心 | 跨节点应用表格：节点/类型/名称/版本/状态；按探测器、类型、节点、状态筛选 | 应用变化时间线、差异高亮 |
| `/commands` | 指令中心 | 上半：发起 exec（选节点 + 命令 + 超时）；下半：全部指令历史（状态筛选 + 分页）；行点击展开抽屉：stdout/stderr（`<pre>` 转义）、exit_code、config.apply 的 stages 时间线 | log_tail 实时流面板、重发按钮（lost/failed） |
| `/nodes/:id/configs/:configId` | 配置版本管理 | 版本时间线（左）+ 内容查看/编辑（右）；编辑器 CodeMirror 6；「保存为新版本」「下发」「回滚」按钮均带**二次确认弹窗** | 双栏 diff 对比 |
| `/system` | 系统 | 面板信息、版本；审计日志表（只读） | TOTP 设置、会话管理 |

### 4.2 关键交互规范

- **二次确认弹窗**（exec / config.apply / 节点删除，见 05 §3.3）：弹窗必须展示"目标节点名 + 操作摘要"（命令全文 / 配置路径+版本 / 节点名），确认按钮需明确文字（"执行"/"下发"/"删除"），禁用歧义词（"确定"）
- **命令输出渲染**：一律 `<pre>{{ text }}</pre>` 转义，禁用 `v-html`（05 §3.6）；超限截断展示"已截断"标记
- **危险状态色**：指令 failed/lost、节点 offline、配置校验失败用统一警示色；rollback 成功单独提示"已自动回滚"
- **空态引导**：节点列表为空 → 引导发放 Token；无配置目标 → 引导从应用清单创建

### 4.3 前端工程约定

- 目录 `web/src/views/` 与路由一一对应；`web/src/api/` 按模块封装 REST 调用；`web/src/ws/` 事件订阅
- 状态管理：Pinia，仅三类 store（nodes、commands、auth）——面板数据以"页面拉取为主、WS 增量刷新"，不做全局大缓存
- 编辑器：**CodeMirror 6**（按 nginx/caddy 语法高亮）；不用 Monaco（体积与二进制 embed 冲突，02 §1.2 的静态 embed 约束）
- CSP 兼容（05 §3.2）：无内联 script/style；构建产物 SRI 由发布流程校验
- P0 无仪表盘页，`/` 重定向 `/nodes`（仪表盘列 P1，见 03）

## 5. P1 / P2 预留

| 阶段 | API/页面扩展 |
|---|---|
| P1 | TOTP 端点与登录页扩展；`docker.op` 指令类型与节点详情容器面板；`log_tail` 流式面板（04 §6 分片 + 浏览器 WS 转发）；监控曲线端点 `GET /nodes/{id}/metrics?from=&to=`；仪表盘页；会话管理 |
| P2 | 多用户管理端点与页面（RBAC）；配置批量下发的编排 API；告警 webhook 配置页 |

## 6. 与其他文档的联动

- 会话/Cookie/限流/CSP 的安全细则以 [05](05-安全设计.md) §3.2/§3.6 为准，本文不重复展开
- 指令类型、payload、状态机以 [04](04-通信协议规范.md) §4/§5 为准；面板 API 的 `commands` 资源是 04 指令状态机的 HTTP 投影
- 配置版本状态机（draft/applied/rolled_back）以 [01](01-产品总体设计.md) §6 为准
- P0 页面清单以 [03](03-迭代路线图.md) §2.1 为准（本文将"系统（审计查看）"细化补充进 P0 最小页面集）
