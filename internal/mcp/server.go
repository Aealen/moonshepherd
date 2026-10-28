// Package mcp 面板内嵌 MCP Server(docs/07/07a)。
// Streamable HTTP(单 POST /mcp)+ Bearer Key 认证 + 手写 JSON-RPC 三方法。
// 安全:hash 校验/统一 401/限速/scope×node 硬校验/审计含拒绝(docs/07 §3)。
package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// ---------- Key 生成(keys.go 语义并入,纯函数) ----------

// GenerateKey 生成明文 Key:mcp_ + 43 位 URL-safe(256bit 熵)。
func GenerateKey() (plain, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	plain = "mcp_" + base64.RawURLEncoding.EncodeToString(b)[:43]
	return plain, HashKey(plain), nil
}

// HashKey sha256 hex(与 store.HashMcpKey 同实现,mcp 包内自足)。
func HashKey(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

// ---------- 限速(loginLimiter 同模式,独立实例) ----------

type limiter struct {
	mu      sync.Mutex
	window  int64 // 当前分钟
	counts  map[string]int
	maxFail int
}

func newLimiter(maxFail int) *limiter {
	return &limiter{counts: map[string]int{}, maxFail: maxFail}
}

// allow 失败计数超限则拒(成功调用不占额度,见 server.go)。
func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	min := time.Now().Unix() / 60
	if min != l.window {
		l.window = min
		l.counts = map[string]int{}
	}
	l.counts[ip]++
	return l.counts[ip] <= l.maxFail
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.counts, ip)
}

// ---------- JSON-RPC 常量与信封 ----------

const protocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var (
	errParse        = &rpcError{Code: -32700, Message: "Parse error"}
	errMethod       = &rpcError{Code: -32601, Message: "Method not found"}
	errInvalid      = &rpcError{Code: -32602, Message: "Invalid params"}
	errInternal     = &rpcError{Code: -32603, Message: "Internal error"}
	errUnauthorized = &rpcError{Code: -32001, Message: "unauthorized"}
)

// ---------- 节点范围(两级:节点→应用;docs/07 §3.2 细化) ----------

// NodeScope 结构化授权范围(存 node_scope JSON;兼容旧格式)。
type NodeScope struct {
	Mode  string       `json:"mode"`  // all / custom
	Nodes []ScopeNode  `json:"nodes"` // custom 时生效
}

// ScopeNode 单节点:Apps 为空 = 该节点全部应用;列出部分 = 仅限这些应用。
type ScopeNode struct {
	ID   string   `json:"id"`
	Apps []string `json:"apps,omitempty"` // 空 = 全部应用
}

// ParseNodeScope 解析:新格式(mode/nodes)或旧格式(字符串数组→节点全选)。
func ParseNodeScope(raw string) *NodeScope {
	if raw == "all" || raw == "" {
		return &NodeScope{Mode: "all"}
	}
	// 新格式
	var ns NodeScope
	if err := json.Unmarshal([]byte(raw), &ns); err == nil && ns.Mode != "" {
		return &ns
	}
	// 旧格式:["n_xxx",...]
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		nodes := make([]ScopeNode, 0, len(list))
		for _, id := range list {
			nodes = append(nodes, ScopeNode{ID: id})
		}
		return &NodeScope{Mode: "custom", Nodes: nodes}
	}
	return &NodeScope{Mode: "custom"}
}

// EncodeNodeScope 序列化。
func EncodeNodeScope(ns *NodeScope) string {
	b, _ := json.Marshal(ns)
	return string(b)
}

// ---------- scope 定义(docs/07 §3.2) ----------

const (
	ScopeRead         = "read"
	ScopeLogs         = "logs"
	ScopeContainerOps = "container-ops"
	ScopeFiles        = "files"
	ScopeExec         = "exec"
)

// AllScopes 配置页展示序(风险升序)。
var AllScopes = []string{ScopeRead, ScopeLogs, ScopeContainerOps, ScopeFiles, ScopeExec}

// HasScope key.scopes 含目标?
func HasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// AllowTarget 两级校验:节点在范围内,且(带 appName 时)应用也在。
// appName 为空 = 节点级操作,只要节点在范围即可。
// 双格式兼容走 ParseNodeScope(旧字符串数组 = 节点全选)。
func AllowTarget(nodeScopeJSON, nodeID, appName string) bool {
	parsed := ParseNodeScope(nodeScopeJSON)
	if parsed.Mode == "all" {
		return true
	}
	for _, n := range parsed.Nodes {
		if n.ID != nodeID {
			continue
		}
		if appName == "" || len(n.Apps) == 0 { // 应用通配(节点全选)或无应用语义
			return true
		}
		for _, a := range n.Apps {
			if a == appName {
				return true
			}
		}
		return false
	}
	return false
}

// ValidScopes 入参 scopes 全部合法(配置入口校验)。
func ValidScopes(scopes []string) bool {
	valid := map[string]bool{}
	for _, s := range AllScopes {
		valid[s] = true
	}
	for _, s := range scopes {
		if !valid[s] {
			return false
		}
	}
	return true
}

// ValidNodeScope 兼容三种:all / 旧字符串数组 / 新 {mode,nodes} 结构。
func ValidNodeScope(ns string) bool {
	if ns == "all" || ns == "" {
		return true
	}
	var list []string
	if json.Unmarshal([]byte(ns), &list) == nil {
		return true
	}
	p := ParseNodeScope(ns)
	return p.Mode == "custom"
}
