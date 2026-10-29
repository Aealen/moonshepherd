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
	ProtoMajor   = 1
	ProtoMinor   = 0
	ProtoVersion = "1.0"
)

// 消息类型（docs/04 §5）。
const (
	TypeRegister      = "register"
	TypeRegisterAck   = "register_ack"
	TypeHello         = "hello"
	TypeHelloAck      = "hello_ack"
	TypeHeartbeat     = "heartbeat"
	TypeReport        = "report"
	TypeCommand       = "command"
	TypeCommandResult = "command_result"
	TypeError         = "error"
	// P1 预留
	TypeStreamChunk = "stream_chunk"
	TypeStreamStop  = "stream_stop"
	// 交互式终端会话（方案 B：Web 终端，docs/04 §7）
	TypeSessionOpen    = "session.open"     // P→A 打开 PTY 会话
	TypeSessionOpenAck = "session.open_ack" // A→P 打开结果
	TypeSessionInput   = "session.input"    // P→A 终端输入（base64）
	TypeSessionOutput  = "session.output"   // A→P 终端输出（base64）
	TypeSessionResize  = "session.resize"   // P→A 窗口尺寸变化
	TypeSessionClose   = "session.close"    // 双向 会话关闭
	// 容器日志实时流(docs/04 §9:tail -f 语义,弹窗直读不经指令中心)
	TypeLogStreamStart = "log.stream.start" // P→A 开始跟随容器日志
	TypeLogStreamChunk = "log.stream.chunk" // A→P 日志增量(base64)
	TypeLogStreamStop  = "log.stream.stop"  // 双向 停止跟随
	// 远端文件只读查看(docs/04 §10:白名单文本配置,一问一答)
	TypeFileRead    = "file.read"     // P→A 读文件
	TypeFileReadAck = "file.read.ack" // A→P 内容/错误
	// 文件管理器(docs/04 §11:树浏览 + 常用操作,一问一答 + 上传分片)
	TypeFileOp     = "file.op"     // P→A list/write/mkdir/delete/move/copy/rename
	TypeFileOpAck  = "file.op.ack" // A→P 操作结果
	TypeFileUploadChunk = "file.upload.chunk" // P→A 上传分片(base64)
	TypeFileUploadDone  = "file.upload.done"  // P→A 上传完成(校验+就位)
)

// Agent capabilities（register.capabilities；面板按能力开放功能）。
const (
	CapProbe  = "probe"  // 应用探测（M3）
	CapExec   = "exec"   // 远程命令（M2）
	CapPty    = "pty"    // 交互式终端（Web 终端）
	CapDocker = "docker" // docker.op 容器生命周期白名单操作
)

// 指令类型（docs/04 §5.3）。
const (
	CmdExec        = "exec"
	CmdProbe       = "probe"
	CmdConfigApply = "config.apply"
	CmdDockerOp    = "docker.op"    // 容器生命周期白名单操作(P1 已实现)
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
	RegistrationToken string   `json:"registration_token"`
	Hostname          string   `json:"hostname"`
	OS                OSInfo   `json:"os"`
	AgentVersion      string   `json:"agent_version"`
	ProtoVersion      string   `json:"proto_version"`
	Capabilities      []string `json:"capabilities"`
}

type OSInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

// RegisterAckPayload 面板回执：node_secret 唯一一次明文下发（TLS 保护下）。
type RegisterAckPayload struct {
	NodeID     string      `json:"node_id"`
	NodeSecret string      `json:"node_secret"`
	Config     AgentConfig `json:"config"`
}

// HelloPayload 每次连接的第一条消息：HMAC 挑战认证（docs/04 §2.3）。
// AgentVersion/ProtoVersion/Capabilities 可选上报(v0.1.0+):Agent 换二进制后重连即
// 刷新面板节点表(版本/能力不再停留注册时旧值);老 Agent 不带,面板保留库中原值。
type HelloPayload struct {
	NodeID string `json:"node_id"`
	TS     int64  `json:"ts"`
	Nonce  string `json:"nonce"`
	Sig    string `json:"sig"`
	AgentVersion string   `json:"agent_version,omitempty"`
	ProtoVersion string   `json:"proto_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
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
	// Agent 自更新(docs/01 §3.5):ExpectedVersion 非空且 ≠ 自身版本时,
	// Agent 在 UI/心跳中标记待升级;真正升级仍需面板下发 agent.update 指令(一期手动触发)。
	ExpectedVersion string `json:"expected_version,omitempty"`
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

// DockerOpPayload docker.op 白名单操作(docs/01 §3.4:枚举操作,非任意命令)。
// Container 为容器名或 ID(探测器上报的 name/id 均可)。
type DockerOpPayload struct {
	Op        string `json:"op"`                  // start/stop/restart/logs/pull
	Container string `json:"container,omitempty"` // logs/pull 外必填
	TailLines int    `json:"tail_lines,omitempty"`
	Image     string `json:"image,omitempty"` // pull 用:缺省取容器当前镜像
}

// docker.op 白名单操作集(Agent 侧硬校验,面板侧复用同一份)。
const (
	DockerOpStart   = "start"
	DockerOpStop    = "stop"
	DockerOpRestart = "restart"
	DockerOpLogs    = "logs"
	DockerOpPull    = "pull"
)

// DockerOpsAllowed 白名单(顺序即面板操作列展示序)。
var DockerOpsAllowed = []string{DockerOpStart, DockerOpStop, DockerOpRestart, DockerOpLogs, DockerOpPull}

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

// ---------- 交互式终端会话（docs/04 §7） ----------

// SessionOpenPayload 面板请求打开 PTY 会话；session_id 由面板生成（审计关联）。
type SessionOpenPayload struct {
	SessionID string `json:"session_id"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

// SessionOpenAckPayload Agent 回执：ok=false 时携带原因（并发上限/系统不支持等）。
type SessionOpenAckPayload struct {
	SessionID string `json:"session_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// SessionIOPayload 终端输入/输出，Data 为原始终端字节流的 base64
// （PTY 输出可为任意字节含非 UTF-8 序列，文本帧 JSON 无法安全承载，docs/04 §7.2）。
type SessionIOPayload struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data_b64"`
}

// SessionResizePayload 终端窗口尺寸变化（行/列），Agent 转 SIGWINCH。
type SessionResizePayload struct {
	SessionID string `json:"session_id"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

// SessionClosePayload 会话关闭（双向）；Reason 供审计：user_close / idle_timeout /
// conn_closed / exit / limit / error。
type SessionClosePayload struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason,omitempty"`
}

// 终端会话约束（docs/04 §7.1；Agent 侧硬限制，面板侧另有会话数限制）。
const (
	MaxSessionsPerAgent = 3       // 每 Agent 并发会话上限
	SessionIdleTimeoutS = 15 * 60 // 无输入无输出空闲超时（秒）
)

// ---------- 容器日志实时流（docs/04 §9） ----------

// LogStreamStartPayload 面板请求跟随容器日志:先回 tail_lines 历史再持续增量。
type LogStreamStartPayload struct {
	StreamID  string `json:"stream_id"` // 面板生成,审计关联
	Container string `json:"container"`
	TailLines int    `json:"tail_lines,omitempty"` // 缺省 100
}

// LogStreamChunkPayload 日志增量;EOF 时容器已停止跟随(end=true)。
type LogStreamChunkPayload struct {
	StreamID string `json:"stream_id"`
	Data     string `json:"data_b64"`
	End      bool   `json:"end,omitempty"`
}

// LogStreamStopPayload 停止跟随(双向);Reason: user_close / conn_closed / limit / error。
type LogStreamStopPayload struct {
	StreamID string `json:"stream_id"`
	Reason   string `json:"reason,omitempty"`
}

// 日志流约束(Agent 侧硬限制)。
const (
	MaxLogStreamsPerAgent = 5       // 并发日志跟随上限
	LogStreamTailMax      = 1000    // tail 上限
	LogStreamTimeoutMin   = 15 * 60 // 跟随最长时长(分钟),防遗忘泄漏
)

// ---------- 远端文件只读查看(docs/04 §10) ----------

// FileReadPayload 面板请求读取文件内容(只读,白名单校验在 Agent)。
type FileReadPayload struct {
	RequestID string `json:"request_id"`
	Path      string `json:"path"`
}

// FileReadAckPayload 文件内容(文本原样;二进制/超限/越权回 error)。
type FileReadAckPayload struct {
	RequestID string `json:"request_id"`
	Content   string `json:"content,omitempty"`
	Size      int64  `json:"size,omitempty"`      // 文件原始大小(字节)
	Truncated bool   `json:"truncated,omitempty"` // 超 MaxFileReadBytes 截断
	Error     string `json:"error,omitempty"`
}

// 文件查看约束(Agent 侧硬限制;docs/05 安全:只读文本配置)。
const (
	MaxFileReadBytes = 512 << 10 // 512KB,超出截断
)

// ---------- 文件管理器(docs/04 §11) ----------

// FileOpPayload P→A 操作请求。Op 枚举:
//
//	list    {path}                      → 目录条目(Entries)
//	write   {path, content}             → 原子写文本(临时文件+rename)
//	mkdir   {path}
//	delete  {path}                      → 文件或空目录;递归需 recursive=true
//	move    {path, dest}                → 同文件系统 rename;跨区失败提示复制+删
//	copy    {path, dest}                → 文件复制(目录递归)
//	rename  {path, dest}                → 同 move(语义别名,UI 区分展示)
type FileOpPayload struct {
	RequestID string `json:"request_id"`
	Op        string `json:"op"`
	Path      string `json:"path"`
	Dest      string `json:"dest,omitempty"`
	Content   string `json:"content,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
}

// FileEntry 目录条目(list 应答)。
type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"dir"`
	Size    int64  `json:"size,omitempty"`
	Mode    string `json:"mode,omitempty"`     // 如 -rw-r--r-- / drwxr-xr-x
	ModTime int64  `json:"mtime,omitempty"`    // Unix 秒
	NoPerm  bool   `json:"no_perm,omitempty"`  // 无法读取(占位提示,不报错)
}

// FileOpAckPayload 操作应答:成功按 Op 带结果,失败带 Error(含权限语义)。
type FileOpAckPayload struct {
	RequestID string      `json:"request_id"`
	OK        bool        `json:"ok"`
	Entries   []FileEntry `json:"entries,omitempty"` // list
	Error     string      `json:"error,omitempty"`
	// 权限语义:denied=true 表示 EACCES/EPERM(前端弹"权限不足"而非"出错")
	Denied bool `json:"denied,omitempty"`
}

// FileUploadChunkPayload 上传分片:同 SessionID 顺序累积,Done 校验就位。
type FileUploadChunkPayload struct {
	SessionID string `json:"session_id"` // 面板生成
	Seq       int    `json:"seq"`
	Data      string `json:"data_b64"`
	Path      string `json:"path,omitempty"` // 首片(seq=0)必带:目标绝对路径
}

// FileUploadDonePayload 上传完成:sha256 校验通过才原子就位到 Path。
type FileUploadDonePayload struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"` // 目标绝对路径
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// 文件管理约束(Agent 侧硬限制)。
const (
	MaxFileWriteBytes    = 512 << 10  // 单次写/编辑上限
	MaxFileUploadBytes   = 256 << 20  // 单文件上传上限 256MB
	FileUploadChunkBytes = 240 << 10  // 分片 ≤240KB(base64 后 < 1MiB 单帧)
)

// ---------- Agent 自更新(docs/01 §3.5;docs/04 §5.3 P1 落地) ----------

// AgentUpdatePayload agent.update 指令 payload。
// DownloadURL 必须为 https(或面板自身下发通道 /dist/…),sha256 强制校验。
type AgentUpdatePayload struct {
	Version     string `json:"version"`      // 目标版本(更新后 version 输出)
	DownloadURL string `json:"download_url"` // 二进制下载地址
	SHA256      string `json:"sha256"`       // 目标二进制校验和(hex)
}

// AgentUpdateResultPayload agent.update 回执(经 command_result.stdout/stderr 承载明细)。
const (
	UpdateSupervisorSystemd = "systemd" // systemd unit 托管:校验后 systemctl restart
	UpdateSupervisorBare    = "bare"    // 裸跑(nohup/User Scripts):syscall.Exec 原地接管
	UpdateSupervisorUnknown = "unknown" // 无法判定:拒绝自动更新(防失联)
)

// 更新安全约束。
const (
	UpdateDownloadMaxBytes = 64 << 20 // 二进制下载上限 64MB
)

// ---------- 错误（docs/04 §5.5/§5.6） ----------

type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	RefID   string `json:"ref_id,omitempty"`
}

// 错误码表（docs/04 §5.6）。
const (
	ErrBadToken         = 1001 // registration_token 无效/已用
	ErrTokenExpired     = 1002
	ErrAuthFailed       = 1003 // hello 认证失败
	ErrNonceReplay      = 1004
	ErrUnknownType      = 2001
	ErrBadPayload       = 2002
	ErrCmdUnsupported   = 2003
	ErrNodeDisabled     = 3001
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
