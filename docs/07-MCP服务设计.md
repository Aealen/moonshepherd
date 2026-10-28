# 07 - MCP 服务设计(MoonShepherd · 牧云)

> 状态:设计定稿待评审
> 关联:docs/01 §3(架构)、docs/05(安全)、docs/06(面板 API)
> 原则:**MCP 技术不难,难在安全风险把控**——本文档以风险驱动设计,每个能力暴露都对应明确的 scope 与审计。

## 1. 目标与定位

把牧云面板变为 **AI 可编程的运维平面**:任意 MCP 客户端(Claude Desktop/Zed/Cursor 等)可通过自然语言驱动牧云能力——"哪个节点磁盘快满了"、"重启 Gitea 容器"、"看下 nginx 日志有没有 5xx"。

**非目标**(一期明确不做):

- PTY 交互式终端(有状态会话与 MCP request/response 模型不匹配;"AI 拿 shell"是独立风险等级,exec scope 已覆盖 95% 需求)
- MCP Resources/Widgets(仅 tools)
- 多用户 MCP(一期单面板管理员维度;Key 即身份)

## 2. 架构

```
┌─────────────────┐  Streamable HTTP (POST /mcp)  ┌──────────────────────────┐
│  MCP 客户端       │ ───────────────────────────► │  面板进程(内嵌 MCP Server) │
│  Claude/Zed/...  │   Bearer: mcp_xxx(明文 Key)    │                          │
└─────────────────┘                               │  ┌────────────────────┐  │
                                                  │  │ 认证:Key hash 校验    │  │
                                                  │  │ 授权:scope + node 范围 │  │
                                                  │  │ 审计:mcp.<tool> 落库  │  │
                                                  │  └─────────┬──────────┘  │
                                                  │            ▼ 复用既有逻辑    │
                                                  │  nodes/apps/logs/docker/    │
                                                  │  files/commands handlers    │
                                                  │            │                │
                                                  │            ▼ WSS 网关       │
                                                  └──────────────┬─────────────┘
                                                                 ▼
                                                              Agent(节点)
```

要点:

1. **零新部署物**:MCP Server 编译进面板二进制(路由挂 `/mcp`),面板镜像升级即得,与 Agent 无关(Agent 不感知 MCP)。
2. **复用而非旁路**:tools 内部直接调用既有 store/hub/handler 逻辑(同进程函数调用),不经过 HTTP 自环——保证与面板行为完全一致,审计/白名单/校验单点维护。
3. **传输**:Streamable HTTP(单 POST 端点,JSON-RPC 2.0),不用 SSE 旧模式;无会话粘性要求(每请求独立鉴权)。

## 3. 安全模型(核心)

### 3.1 身份:MCP Key

| 属性 | 设计 |
|---|---|
| 格式 | `mcp_` + 43 位 URL-safe 随机(256bit 熵) |
| 存储 | **仅存 SHA-256 hash**(与登录密码/注册 Token 同哲学);明文只在创建响应里出现一次 |
| 传输 | `Authorization: Bearer mcp_xxx` |
| 名称 | 必填,审计中显示(如 "claude-desktop"、"测试机专用") |
| 吊销 | 即时(删 Key 即断);支持"吊销但保留审计记录" |
| 数量上限 | 每面板 10 把(防 Key 农场式滥用) |
| 失效保护 | Key 校验失败统一返回 401,不区分"不存在/已吊销"(防枚举);连续失败同登录限速(5 次/分钟/IP) |

### 3.2 授权:scope × node 范围(每 Key 独立配置)

**能力 scope**(勾选制,默认全关):

| scope | 允许的 tools | 风险级 | UI 提示 |
|---|---|---|---|
| `read` | list_nodes / get_node_metrics / list_apps / get_panel_info | 🟢 零(纯只读) | — |
| `logs` | get_container_logs | 🟢 低(只读,可能含敏感信息) | "日志可能包含密钥等敏感内容" |
| `container-ops` | container_op(白名单同 docker.op) | 🟡 中 | "可启停/重启/拉取容器" |
| `files` | file_read / file_list / file_write | 🔴 高 | "读写节点文件系统" |
| `exec` | exec_command | 🔴 **最高** | 红色警示"等同把节点 shell 交给 AI",需输入 Key 名二次确认 |

**节点范围**:全部节点 / 指定节点 ID 列表。tool 入口校验 `targetNode ∈ allowedNodes`。

**校验位置**:每个 tool handler 入口**强制**做 `checkScope(key, scope)` + `checkNode(key, nodeID)`,缺一即拒——与 Agent 侧 docker.op 白名单同一哲学:**面板(此处是 MCP 层)说什么都不能越过这道闸**。

### 3.3 审计

每次 MCP tool 调用落 `audit_logs`:

- `action = mcp.<tool_name>`(如 `mcp.container_op`)
- `username = mcp:<Key名>`(如 `mcp:claude-desktop`)
- `detail` = 入参摘要(命令全文/路径/容器名;**文件内容不记**,同文件管理器)
- `ip` = MCP 客户端来源 IP

被 scope 拒绝的调用**也记录**(action 加 `.denied` 后缀)——审计 AI 的越权尝试本身是安全价值。

### 3.4 威胁模型清单(设计自查)

| 威胁 | 对策 |
|---|---|
| Key 泄漏 | 可即时吊销;scope 最小化;hash 存储(拖库拿不到明文) |
| AI 幻觉误操作 | 高危 scope 默认关;exec 开启需 Key 名二次确认;变更类操作审计可追溯 |
| 越权访问未授权节点 | tool 层 node 范围硬校验 |
| 暴力猜 Key | 256bit 熵 + 限速 + 统一 401 |
| 审计绕过 | MCP 与面板同进程,无旁路路径 |
| 重放 | 一期 HTTP Bearer(依赖 TLS);后续可加 nonce/timestamp(记入二期) |
| 提示注入导致 AI 滥用 exec | 文档建议:生产 Key 不开 exec;exec Key 仅绑测试节点 |

## 4. MCP Tools 一期(12 个)

| tool | 入参 | 出参 | 依赖 scope |
|---|---|---|---|
| `list_nodes` | — | 节点列表(含在线状态/版本/待升级标记) | read |
| `get_node_metrics` | node_id | CPU/内存/磁盘/负载最近快照 | read |
| `list_apps` | node_id? / probe? | 应用清单(可跨节点) | read |
| `get_panel_info` | — | 面板版本/节点统计 | read |
| `get_container_logs` | node_id, container, tail? | 日志文本(尾部 N 行,非流式) | logs |
| `container_op` | node_id, container, op(白名单) | 操作结果 | container-ops |
| `exec_command` | node_id, cmd, timeout? | stdout/stderr/exit_code(复用指令状态机) | exec |
| `file_list` | node_id, path | 目录条目 | files |
| `file_read` | node_id, path(白名单闸同文件查看) | 文件内容 | files |
| `file_write` | node_id, path, content(≤512KB,原子写) | 结果 | files |
| `list_configs` | node_id | 配置目标清单 | read |
| `get_config_version` | target_id, version? | 版本内容 | read |

命名/出入参对齐 MCP 惯例(snake_case、结构化 JSON、描述字段写给 LLM 看)。

## 5. 面板配置页

**系统 → MCP Keys**(新分区;独立页面同操作审计模式):

- Key 列表:名称/创建时间/最后使用/scope 徽章/节点范围/状态
- 创建:名称 + scope 勾选(高危红色警示,exec 需输入 Key 名确认)+ 节点范围(全部/勾选)→ **明文仅弹窗显示一次**(复制按钮)
- 编辑:仅可改名称/scope/节点范围/启停(**不可改 Key 值**)
- 吊销:二次确认,审计保留
- 使用说明折叠区:各客户端配置示例(Claude Desktop JSON / 通用 endpoint)

## 6. REST API(面板侧,管理员 Cookie 认证)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/mcp/keys | 列表(不含明文/hash) |
| POST | /api/v1/mcp/keys | 创建(响应含一次性明文) |
| PATCH | /api/v1/mcp/keys/{id} | 改名/scope/nodes/启停 |
| DELETE | /api/v1/mcp/keys/{id} | 吊销 |
| POST | /mcp | MCP Streamable HTTP 入口(Bearer Key) |

## 7. 技术选型

- SDK:`modelcontextprotocol/go-sdk`(官方 Go 实现;若 API 不稳,降级为手写 JSON-RPC 2.0 三方法 `initialize/tools/list/tools/calle`——MCP 核心面很小,手写成本可控且零依赖风险)
- 依赖:零新增第三方(sha256/限速复用面板既有)
- 存储:`mcp_keys` 表(见 07 §8)

## 8. 数据模型

```sql
CREATE TABLE IF NOT EXISTS mcp_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,        -- 审计显示名
    key_hash    TEXT NOT NULL UNIQUE,        -- sha256 hex
    scopes      TEXT NOT NULL DEFAULT '[]',  -- JSON: ["read","logs",...]
    node_scope  TEXT NOT NULL DEFAULT 'all', -- 'all' 或 JSON 数组 ["n_xxx"]
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_used   INTEGER NOT NULL DEFAULT 0,  -- 最近调用(展示/判断活性)
    created_at  INTEGER NOT NULL
);
```

## 9. 迭代切分

| 期 | 内容 |
|---|---|
| 一期(本 PR) | Key 体系 + 配置页 + 12 tools + scope/node 校验 + 审计 + 限速 |
| 二期 | Key 自动过期、调用频率上限(每 Key QPS)、nonce 防重放、MCP 资源(只读清单作为 resources 暴露) |
| 远期 | 按工具粒度的更细授权、MCP 会话级临时 Key(AI 任务结束时自动失效) |

## 10. 验收清单

- [ ] 无 scope 的 Key 调任何 tool 均被拒,且产生 `.denied` 审计
- [ ] Key 明文只在创建响应出现一次;库里只有 hash
- [ ] 节点范围校验:授权列表外节点调用被拒
- [ ] exec scope 开启需 Key 名二次确认(UI)
- [ ] 吊销后 Key 立即 401
- [ ] MCP 审计在操作审计页可见(username=mcp:xxx)
- [ ] Claude Desktop 实配联调通过
