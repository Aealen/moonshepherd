// HTTP 入口与 JSON-RPC 分发(docs/07a §3)。
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aealen/moonshepherd/internal/store"
)

// PanelDeps 面板能力注入(mcp 包不直接依赖 server 包,避免循环引用)。
type PanelDeps interface {
	// Store 数据层
	Store() *store.Store
	// Audit MCP 审计(username 由 mcp 包拼 mcp:xxx)。
	Audit(action, target, detailJSON, ip string, keyName string)
	// NodeOnline 节点在线?
	NodeOnline(nodeID string) bool
	// PanelInfo 面板信息文本
	PanelInfo(ctx context.Context) (string, error)
	// ListConfigsText 配置目标清单文本
	ListConfigsText(ctx context.Context, nodeID string) (string, error)
	// GetConfigText 配置版本内容
	GetConfigText(ctx context.Context, targetID, version int64) (string, error)
	// RunDockerOp 容器操作(同步等回执):op∈logs/start/stop/restart/pull
	RunDockerOp(ctx context.Context, nodeID, op, container string, tail int) (string, error)
	// RunExec 执行命令(同步等回执)
	RunExec(ctx context.Context, nodeID, cmd string, timeoutS int) (string, error)
	// FileOp 文件操作(同步等回执):op∈list/read/write
	FileOp(ctx context.Context, nodeID, op, path, content, dest string) (string, error)
}

// Server MCP 服务体。
type Server struct {
	deps    PanelDeps
	limiter *limiter
}

func NewServer(deps PanelDeps) *Server {
	return &Server{deps: deps, limiter: newLimiter(5)}
}

// HandleHTTP POST /mcp 入口:认证 → 限速 → JSON-RPC。
// 统一 401(不存在/已吊销/禁用不区分,防枚举)。
func (s *Server) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIPOf(r)
	auth := r.Header.Get("Authorization")
	plain, ok := strings.CutPrefix(auth, "Bearer ")
	var key *store.McpKey
	if ok && strings.HasPrefix(plain, "mcp_") {
		k, err := s.deps.Store().McpKeyByHash(r.Context(), HashKey(plain))
		if err == nil && k != nil && k.Enabled {
			key = k
		}
	}
	if key == nil {
		if !s.limiter.allow(ip) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, rpcRespErr(nil, errUnauthorized))
		return
	}
	s.limiter.reset(ip)
	// last_used 异步更新
	go s.deps.Store().TouchMcpKey(context.Background(), key.ID)

	// JSON-RPC
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, rpcRespErr(nil, errParse))
		return
	}
	resp := s.dispatch(r.Context(), key, ip, req)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// dispatch 三方法分发(initialize / tools/list / tools/call)。
func (s *Server) dispatch(ctx context.Context, key *store.McpKey, ip string, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcRespOK(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "moonshepherd", "version": "0.1.0"},
		})
	case "tools/list":
		tools := make([]map[string]any, 0, len(registry))
		for _, t := range registry {
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		return rpcRespOK(req.ID, map[string]any{"tools": tools})
	case "tools/call":
		return s.callTool(ctx, key, ip, req)
	default:
		return rpcRespErr(req.ID, errMethod)
	}
}

// callTool 单工具调用:scope → node → handler → MCP result 语义。
func (s *Server) callTool(ctx context.Context, key *store.McpKey, ip string, req rpcRequest) rpcResponse {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		return rpcRespErr(req.ID, errInvalid)
	}
	t, ok := registry[p.Name]
	if !ok {
		return rpcRespErr(req.ID, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name})
	}

	// scope 硬闸(docs/07 §3.2:框架级,不依赖 handler 自觉)
	if !HasScope(key.Scopes, t.Scope) {
		s.deps.Audit("mcp."+p.Name+".denied", "", `{"reason":"scope"}`, ip, key.Name)
		return rpcRespOK(req.ID, toolError("未授权的 scope: "+t.Scope+"(请联系管理员调整 MCP Key 配置)"))
	}
	// 两级范围硬闸(docs/07 §3.2 细化):节点级 + 应用级
	// 容器操作/日志面向"应用";exec/文件/指标面向"节点"(命令可触达一切,应用绑定无意义)
	var args struct {
		NodeID    string `json:"node_id"`
		Container string `json:"container"`
	}
	_ = json.Unmarshal(p.Arguments, &args)
	if args.NodeID != "" {
		appName := ""
		if t.Scope == ScopeContainerOps || t.Scope == ScopeLogs {
			appName = args.Container
		}
		if !AllowTarget(key.NodeScope, args.NodeID, appName) {
			s.deps.Audit("mcp."+p.Name+".denied", args.NodeID, `{"reason":"scope_target","target":"`+args.Container+`"}`, ip, key.Name)
			if appName != "" {
				return rpcRespOK(req.ID, toolError("应用不在该 Key 的授权范围: "+args.Container+"@"+args.NodeID))
			}
			return rpcRespOK(req.ID, toolError("节点不在该 Key 的授权范围: "+args.NodeID))
		}
		if !s.deps.NodeOnline(args.NodeID) {
			return rpcRespOK(req.ID, toolError("节点不在线: "+args.NodeID))
		}
	}

	// 审计(成功路径;内容/文件树不记,变更入参摘要记)
	s.auditCall(p.Name, args.NodeID, p.Arguments, ip, key.Name)

	text, err := t.Handler(ctx, p.Arguments, s.deps)
	if err != nil {
		return rpcRespOK(req.ID, toolError(err.Error()))
	}
	return rpcRespOK(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

// auditCall 入参摘要审计(截断防巨量;文件内容字段跳过)。
func (s *Server) auditCall(tool, nodeID string, args json.RawMessage, ip, keyName string) {
	sum := string(args)
	if len(sum) > 512 {
		sum = sum[:512] + "…"
	}
	if tool == "file_write" { // 内容不记
		var m map[string]any
		_ = json.Unmarshal(args, &m)
		delete(m, "content")
		b, _ := json.Marshal(m)
		sum = string(b)
	}
	detail := `{"args":` + sum + `}`
	if detail == `{"args":}` {
		detail = `{"args":null}`
	}
	s.deps.Audit("mcp."+tool, nodeID, detail, ip, keyName)
}

// toolError MCP 语义:HTTP 200 + isError=true(工具级错误,非协议错误)。
func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func rpcRespOK(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcRespErr(id json.RawMessage, e *rpcError) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: e}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func clientIPOf(r *http.Request) string {
	// 与面板 clientIP 同语义:XFF 首个(面板常前置反代)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	h := r.RemoteAddr
	if i := strings.LastIndexByte(h, ':'); i > 0 {
		h = h[:i]
	}
	return h
}
