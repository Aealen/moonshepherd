# 07a - MCP 实现规范(MoonShepherd · 牧云)

> 面向实现的落地规范:模块划分、接口签名、校验时序、边界行为。
> 设计依据:docs/07-MCP服务设计.md(先读它,风险模型在彼处)。

## 1. 模块与文件

```
internal/mcp/            # 新包:零依赖面板以外内部包(server 内复用)
  server.go              # Streamable HTTP 端点:JSON-RPC 分发 + 认证 + 限速
  keys.go               # Key 生成/hash/校验(纯函数)
  tools.go              # 12 个 tool 定义(名称/描述/入参 schema) + 注册表
  bridge.go             # tool handler → 面板内部逻辑桥接(依赖注入 Server)
internal/server/
  mcp_handler.go         # 面板 REST:/api/v1/mcp/keys CRUD(管理员 Cookie)
internal/store/
  mcp.go                # mcp_keys 表 CRUD + schema
web/src/views/McpKeys.vue        # 配置页(系统菜单下)
web/src/api/index.ts             # 类型与 api 封装增量
```

依赖方向:`mcp → server/store/protocol`(单向);server 路由挂载 `/mcp` 与 `/api/v1/mcp/keys`。

## 2. 认证时序(每个 /mcp POST)

```
1. 提取 Authorization: Bearer mcp_xxxxx(43 字符)
2. sha256 → 查 mcp_keys(hash 命中)
3. enabled? 否 → 401(统一响应,防枚举)
4. 限速表:ip+minute 维度计数 >5 → 429
5. 通过 → 携带 key 记录进入 JSON-RPC 分发
```

**限速实现**:复用 `loginLimiter` 模式(内存 map + 分钟窗口),独立实例 `mcpLimiter`;Key 校验失败计数,成功不占额度(正常客户端每分钟调用远低于 5 次失败)。

## 3. JSON-RPC 分发(手写,零 SDK 依赖)

一期手写核心三方法(SDK 依赖收益低、API 演进风险高;MCP 面小):

```go
// POST /mcp  body: {"jsonrpc":"2.0","id":1,"method":"initialize","params":{...}}
// 支持:initialize / tools/list / tools/call;其余 → -32601 Method not found
// initialize 返回:protocolVersion("2025-06-18") / capabilities.tools / serverInfo
// tools/list 返回:注册表全量(name/description/inputSchema)——**不含 scope 过滤**
//                 (过滤在 call 层做;list 暴露全量便于用户发现能力,调用时拒绝)
// tools/call:校验 scope+node → handler → {"content":[{"type":"text","text":...}]}
// handler 返回 error 时:tool 结果 isError=true(MCP 语义),HTTP 仍 200
```

**决策记录**:tools/list 不过滤的取舍——若按 scope 过滤,AI 用户看不到能力全集难以请求授权;过滤放 call 层 + `.denied` 审计,越权尝试可观测。若实测噪音大,二期加 `list_filter` 开关。

## 4. tools 注册表(签名)

```go
type Tool struct {
    Name        string
    Description string          // 写给 LLM:一句话说明 + 适用场景
    InputSchema map[string]any  // JSON Schema(draft 2020-12 子集:type/properties/required)
    Scopes      []string        // 满足其一即可(and 语义时用多个元素表达不了——本期内每个 tool 恰一个 scope)
    Handler     func(ctx, key *KeyRec, args json.RawMessage) (string, error) // 返回 text 内容
}
```

Handler 桥接规则(bridge.go):

| tool | 内部复用 | 备注 |
|---|---|---|
| list_nodes | st.ListNodes | 输出紧凑表格式 text |
| get_node_metrics | st.LatestMetric | |
| list_apps | st.ListApps | 支持 node/probe 过滤 |
| get_panel_info | st.SystemInfo | |
| get_container_logs | hub.sendTo + logstream 单次语义 | **非流式**:内部走 docker.op logs 指令(等待回执,≤60s),不走 log.stream(流式与 MCP 不匹配) |
| container_op | createCommandFor(docker.op) | 同步等 command_result(轮询 store,≤60s) |
| exec_command | createCommandFor(exec) | 同上;timeout 默认 60s 上限 300s |
| file_list / file_read / file_write | 复用 agentd 语义:hub.sendTo file.op / file.read + pending ack | 面板侧新增轻量 pending map(request_id→chan),复用 events 转推路径 |
| list_configs / get_config_version | st.ListConfigs / GetVersion | |

**同步等待实现**:MCP 是 request/response,而面板→Agent 是异步消息。桥接层用 `pendingAcks map[string]chan Ack`(mcp 包内),gateway 的 ack 分发处(file.op/file.read/command_result)多播一份给 mcp pending。超时 60s 返回工具错误"节点响应超时"。

## 5. Key 生命周期规则

- 创建:`mcp_` + base64url(rand 32B)[:43];响应体含明文一次;**响应头 no-store**
- 校验失败/禁用/删除:统一 `{error:{code:"unauthorized"}}` + HTTP 401
- PATCH 可改:name/scopes/node_scope/enabled(任一);**key_hash 不可改**
- 删除=吊销(硬删行;审计已有记录不依赖该行)
- last_used:每次成功 call 更新(异步,不阻塞请求)

## 6. scope 校验器

```go
// checkScope: key.Scopes 含所需 scope?
// checkNode:  key.NodeScope=="all" 或含 nodeID?
// 两者在每个 tool Handler 入口调用(mcp 包内,不依赖 handler 自觉)
```

被拒时:审计 `mcp.<tool>.denied`(detail 记被拒原因与目标节点),返回 tool 错误("未授权的 scope: exec(请联系管理员调整 Key 配置)")——**错误文案面向 AI 可读**,让 AI 能向用户转述缺什么权限。

## 7. 配置页要点(McpKeys.vue)

- 表格列:名称/状态/最后使用(fmtAgo)/scope 徽章色(read 绿/logs 青蓝/container-ops 金/files 红/exec 深红)/节点范围("全部"或 N 个)
- 创建弹窗:名称必填;scope 勾选组(exec 勾选时**弹二次输入 Key 名确认**);节点范围 radio(全部/勾选节点)
- 创建成功:一次性明文弹窗(等宽字体 + 复制按钮 + "关闭后不再显示"警示)
- 使用说明 n-collapse:Claude Desktop / 通用 HTTP 示例(endpoint=https://面板/mcp,header 示例)

## 8. 测试要点

| 测试 | 断言 |
|---|---|
| keys.go 纯函数 | 生成格式/长度/hash 稳定性 |
| 认证中间件 | 无头/错 Key/禁用/删除 → 401;正确 → 放行且 last_used 更新 |
| scope 拒绝 | 无 exec 的 Key 调 exec_command → tool isError + `.denied` 审计落库 |
| node 拒绝 | node_scope 限定的 Key 调范围外节点 → 拒 |
| JSON-RPC | initialize/tools/list 正常;未知 method → -32601;坏 JSON → -32700 |
| bridge 同步等待 | 假 Agent(既有测试模式)回执 → tool 结果正确;不回执 → 60s 超时文案 |
| REST CRUD | 管理员全通;明文只在 POST 响应 |

## 9. 安全自查实现映射(对照 docs/07 §3.4)

| 威胁 | 代码位置 |
|---|---|
| hash 存储 | store/mcp.go(只写 key_hash) |
| 统一 401 | mcp/server.go 认证函数单出口 |
| 限速 | mcp/server.go(独立 limiter 实例) |
| scope/node 硬校验 | mcp/tools.go Handler 入口(框架级,非自觉) |
| 审计含拒绝 | mcp/tools.go 拒绝分支 |
| no-store | mcp_handler.go 响应头 |
| exec 二次确认 | McpKeys.vue(前端闸)+ 后端不校验(文档明确此为 UX 闸,真闸是 scope) |
