// Package protocol 定义牧云面板与 MoonShepherd Agent 之间的 WSS 消息协议。
// 本包是双端唯一共享代码（docs/02 §2 结构原则），实现依据 docs/04-通信协议规范.md。
package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// 当前协议版本（主.次，见 docs/04 §1）。
const (
	ProtoMajor = 1
	ProtoMinor = 0
	ProtoVersion = "1.0"
)

// 消息类型（docs/04 §5）。
const (
	TypeRegister       = "register"
	TypeRegisterAck    = "register_ack"
	TypeHello          = "hello"
	TypeHelloAck       = "hello_ack"
	TypeHeartbeat      = "heartbeat"
	TypeReport         = "report"
	TypeCommand        = "command"
	TypeCommandResult  = "command_result"
	TypeError          = "error"
	// P1 预留
	TypeStreamChunk = "stream_chunk"
	TypeStreamStop  = "stream_stop"
)

// 指令类型（docs/04 §5.3）。
const (
	CmdExec        = "exec"
	CmdProbe       = "probe"
	CmdConfigApply = "config.apply"
	CmdDockerOp    = "docker.op"    // P1
	CmdAgentUpdate = "agent.update" // P1
)

// 指令回执状态（command_result.status）。
const (
	ResultAccepted = "accepted"
	ResultSuccess  = "success"
	ResultFailed   = "failed"
	ResultExpired  = "expired"
)

// 应用状态枚举（docs/04 §5.2）。
const (
	AppRunning   = "running"
	AppStopped   = "stopped"
	AppInstalled = "installed"
	AppUnknown   = "unknown"
)

// Envelope 是 WSS 文本帧的 JSON 信封（docs/04 §1）。
type Envelope struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	TS      int64  `json:"ts"`
	Payload any    `json:"payload,omitempty"`
}

// MaxPayloadBytes 单帧 payload 上限 1 MiB；超限截断并置 truncated（docs/04 §1）。
const MaxPayloadBytes = 1 << 20

// ---------- 注册与认证（docs/04 §2） ----------

// RegisterPayload Agent 首次注册（一次性 Token 换发节点身份）。
type RegisterPayload struct {
	RegistrationToken string           `json:"registration_token"`
	Hostname          string           `json:"hostname"`
	OS                OSInfo           `json:"os"`
	AgentVersion      string           `json:"agent_version"`
	ProtoVersion      string           `json:"proto_version"`
	Capabilities      []string         `json:"capabilities"`
}

type OSInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

// RegisterAckPayload 面板回执：node_secret 唯一一次明文下发（TLS 保护下）。
type RegisterAckPayload struct {
	NodeID     string       `json:"node_id"`
	NodeSecret string       `json:"node_secret"`
	Config     AgentConfig  `json:"config"`
}

// HelloPayload 每次连接的第一条消息：HMAC 挑战认证（docs/04 §2.3）。
type HelloPayload struct {
	NodeID string `json:"node_id"`
	TS     int64  `json:"ts"`
	Nonce  string `json:"nonce"`
	Sig    string `json:"sig"`
}

// HelloAckPayload 面板回执，可捎带运行配置热更新。
type HelloAckPayload struct {
	ServerTime int64       `json:"server_time"`
	Config     AgentConfig `json:"config"`
}

// AgentConfig 面板下发给 Agent 的运行配置。
type AgentConfig struct {
	HeartbeatIntervalS int `json:"heartbeat_interval_s"`
	ProbeIntervalS     int `json:"probe_interval_s"`
	CommandTTLs        int `json:"command_ttl_s"`
}

// DefaultAgentConfig 面板默认下行配置（可被面板配置覆盖）。
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{HeartbeatIntervalS: 30, ProbeIntervalS: 300, CommandTTLs: 600}
}

// HelloSig 计算 HMAC-SHA256(node_secret, node_id|ts|nonce) 的十六进制签名。
func HelloSig(nodeSecret, nodeID string, ts int64, nonce string) string {
	mac := hmac.New(sha256.New, []byte(nodeSecret))
	fmt.Fprintf(mac, "%s|%d|%s", nodeID, ts, nonce)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHelloSig 恒定时间比较校验签名。
func VerifyHelloSig(nodeSecret string, h HelloPayload) bool {
	return hmac.Equal([]byte(HelloSig(nodeSecret, h.NodeID, h.TS, h.Nonce)), []byte(h.Sig))
}

// ---------- 心跳与上报（docs/04 §5.1/§5.2） ----------

type HeartbeatPayload struct {
	CPUPercent float64       `json:"cpu_percent,omitempty"`
	Mem        *MemSnapshot  `json:"mem,omitempty"`
	Disk       *DiskSnapshot `json:"disk,omitempty"`
	Net        *NetSnapshot  `json:"net,omitempty"`
	Load1      float64       `json:"load1,omitempty"`
	UptimeS    int64         `json:"uptime_s,omitempty"`
}

type MemSnapshot struct {
	UsedMB  int64 `json:"used_mb"`
	TotalMB int64 `json:"total_mb"`
}

type DiskSnapshot struct {
	Path    string `json:"path"`
	UsedGB  int64  `json:"used_gb"`
	TotalGB int64  `json:"total_gb"`
}

type NetSnapshot struct {
	RxKBps float64 `json:"rx_kb_s"`
	TxKBps float64 `json:"tx_kb_s"`
}

type ReportPayload struct {
	ProbeName  string `json:"probe_name"`
	DetectedAt int64  `json:"detected_at"`
	Apps       []App  `json:"apps"`
}

// App 探测器上报的应用实例（原始 detail 原样落库供面板渲染）。
type App struct {
	AppType string `json:"app_type"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status"`
	Detail  any    `json:"detail,omitempty"`
}

// ---------- 指令（docs/04 §5.3/§5.4） ----------

type CommandPayload struct {
	CommandID string `json:"command_id"`
	Type      string `json:"type"`
	TimeoutS  int    `json:"timeout_s"`
	Payload   any    `json:"payload"`
}

type ExecPayload struct {
	Cmd string `json:"cmd"`
}

type ProbePayload struct {
	ProbeNames []string `json:"probe_names"`
}

type ConfigApplyPayload struct {
	AppType    string `json:"app_type"`
	TargetPath string `json:"target_path"`
	Content    string `json:"content"`
	VersionID  int64  `json:"version_id"`
}

type CommandResultPayload struct {
	CommandID string  `json:"command_id"`
	Status    string  `json:"status"`
	ExitCode  int     `json:"exit_code,omitempty"`
	Stdout    string  `json:"stdout,omitempty"`
	Stderr    string  `json:"stderr,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Stages    []Stage `json:"stages,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// Stage 是 config.apply 的阶段回执（backup→write→validate→reload→rollback）。
type Stage struct {
	Name   string `json:"name"`
	Status string `json:"status"` // success / failed / skipped
	Detail string `json:"detail,omitempty"`
}

// ---------- 错误（docs/04 §5.5/§5.6） ----------

type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	RefID   string `json:"ref_id,omitempty"`
}

// 错误码表（docs/04 §5.6）。
const (
	ErrBadToken        = 1001 // registration_token 无效/已用
	ErrTokenExpired    = 1002
	ErrAuthFailed      = 1003 // hello 认证失败
	ErrNonceReplay     = 1004
	ErrUnknownType     = 2001
	ErrBadPayload      = 2002
	ErrCmdUnsupported  = 2003
	ErrNodeDisabled    = 3001
	ErrProtoUnsupported = 3002
)

// 连接关闭码（docs/04 §2.5）。
const (
	CloseDuplicateConn = 4000 // 同节点重复连接，旧连接被踢
	CloseAuthFailed    = 4001
	CloseNodeDisabled  = 4002
	CloseProtoVersion  = 4003
	CloseOverloaded    = 4004
)
