// tools 注册表与桥接(docs/07 §4 / 07a §4)。
// 桥接依赖注入:CommandRunner(同步等回执)/Store(直读);见 bridge 接口。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aealen/moonshepherd/internal/store"
)

// 别名:tools 内引用 store 过滤器(保持短名)。
type (
	storeNodeFilter = store.NodeFilter
	storeAppFilter  = store.AppFilter
)

// Tool 注册项(docs/07a §4)。
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Scope       string
	Handler     func(ctx context.Context, args json.RawMessage, deps PanelDeps) (string, error)
}

// registry 全量工具(tools/list 输出;call 层做 scope 过滤,docs/07a §3 决策)。
var registry = map[string]*Tool{}

func register(t *Tool) { registry[t.Name] = t }

func obj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}
func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func init() {
	// ---------- read ----------
	register(&Tool{
		Name: "list_nodes", Scope: ScopeRead,
		Description: "列出牧云面板的全部受控节点:名称/在线状态/系统/Agent 版本。运维巡检或定位目标节点的第一步。",
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ json.RawMessage, d PanelDeps) (string, error) {
			nodes, _, err := d.Store().ListNodes(ctx, storeNodeFilter{}, 1, 100)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "共 %d 个节点:\n", len(nodes))
			for _, n := range nodes {
				st := "离线"
				if n.Status == "online" {
					st = "在线"
				}
				fmt.Fprintf(&b, "- %s (%s) %s · %s %s · agent %s\n", n.Name, n.ID, st, n.OSName, n.OSArch, n.AgentVersion)
			}
			return b.String(), nil
		},
	})
	register(&Tool{
		Name: "get_node_metrics", Scope: ScopeRead,
		Description: "获取节点最近一次心跳指标:CPU/内存/磁盘/负载。回答\"哪个节点资源紧张\"类问题。",
		InputSchema: obj(map[string]any{"node_id": str("节点 ID(list_nodes 获取)")}, "node_id"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID string `json:"node_id"`
			}
			_ = json.Unmarshal(args, &a)
			m, _, err := d.Store().LatestMetric(ctx, a.NodeID)
			if err != nil || m == nil {
				return "", fmt.Errorf("无指标数据(节点刚接入或离线)")
			}
			memPct := 0
			if m.MemTotal > 0 {
				memPct = int(m.MemUsed * 100 / m.MemTotal)
			}
			return fmt.Sprintf("CPU %d%% · 内存 %d/%dMB(%d%%) · 磁盘 %d/%dGB · load1 %.2f · 网络 rx%dkB/s tx%dkB/s",
				int(m.CPU), m.MemUsed, m.MemTotal, memPct, m.DiskUsed, m.DiskTotal, m.Load1, int(m.NetRx), int(m.NetTx)), nil
		},
	})
	register(&Tool{
		Name: "list_apps", Scope: ScopeRead,
		Description: "列出应用清单(可按节点过滤):容器/系统服务/中间件的名称、类型、运行状态、版本。回答\"谁装了什么\"。",
		InputSchema: obj(map[string]any{
			"node_id": str("可选,按节点过滤"),
			"probe":   str("可选,按探测器过滤:docker/systemd/nginx/caddy"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID string `json:"node_id"`
				Probe  string `json:"probe"`
			}
			_ = json.Unmarshal(args, &a)
			apps, _, err := d.Store().ListApps(ctx, storeAppFilter{NodeID: a.NodeID, Probe: a.Probe}, 1, 200)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "共 %d 个应用:\n", len(apps))
			for _, x := range apps {
				fmt.Fprintf(&b, "- [%s] %s @%s %s %s\n", x.ProbeName, x.AppName, x.NodeName, x.Status, x.Version)
			}
			return b.String(), nil
		},
	})
	register(&Tool{
		Name: "get_panel_info", Scope: ScopeRead,
		Description: "面板自身信息:版本/节点统计/运行时长。",
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ json.RawMessage, d PanelDeps) (string, error) {
			return d.PanelInfo(ctx)
		},
	})
	register(&Tool{
		Name: "list_configs", Scope: ScopeRead,
		Description: "列出节点的配置管理目标(nginx/caddy 等已纳管配置)。",
		InputSchema: obj(map[string]any{"node_id": str("节点 ID")}, "node_id"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID string `json:"node_id"`
			}
			_ = json.Unmarshal(args, &a)
			return d.ListConfigsText(ctx, a.NodeID)
		},
	})
	register(&Tool{
		Name: "get_config_version", Scope: ScopeRead,
		Description: "读取配置目标的最新(或指定)版本内容。",
		InputSchema: obj(map[string]any{
			"target_id": intp("配置目标 ID"),
			"version":   intp("可选,版本号;缺省最新"),
		}, "target_id"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				TargetID int64 `json:"target_id"`
				Version  int64 `json:"version"`
			}
			_ = json.Unmarshal(args, &a)
			return d.GetConfigText(ctx, a.TargetID, a.Version)
		},
	})

	// ---------- logs ----------
	register(&Tool{
		Name: "get_container_logs", Scope: ScopeLogs,
		Description: "读取 Docker 容器日志尾部 N 行(默认 100)。排障首选;日志可能包含敏感信息。",
		InputSchema: obj(map[string]any{
			"node_id":   str("节点 ID"),
			"container": str("容器名"),
			"tail":      intp("尾行数,默认 100,上限 500"),
		}, "node_id", "container"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID    string `json:"node_id"`
				Container string `json:"container"`
				Tail      int    `json:"tail"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Tail <= 0 {
				a.Tail = 100
			}
			if a.Tail > 500 {
				a.Tail = 500
			}
			return d.RunDockerOp(ctx, a.NodeID, "logs", a.Container, a.Tail)
		},
	})

	// ---------- container-ops ----------
	register(&Tool{
		Name: "container_op", Scope: ScopeContainerOps,
		Description: "容器生命周期操作:start/stop/restart/pull(白名单)。重启服务、拉取新镜像用。",
		InputSchema: obj(map[string]any{
			"node_id":   str("节点 ID"),
			"container": str("容器名"),
			"op":        str("start|stop|restart|pull"),
		}, "node_id", "container", "op"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID    string `json:"node_id"`
				Container string `json:"container"`
				Op        string `json:"op"`
			}
			_ = json.Unmarshal(args, &a)
			switch a.Op {
			case "start", "stop", "restart", "pull":
			default:
				return "", fmt.Errorf("op 仅支持 start/stop/restart/pull")
			}
			return d.RunDockerOp(ctx, a.NodeID, a.Op, a.Container, 0)
		},
	})

	// ---------- exec ----------
	register(&Tool{
		Name: "exec_command", Scope: ScopeExec,
		Description: "在节点执行 shell 命令并返回输出(stdout/stderr/退出码)。高危能力:仅管理员明确授权的 Key 可用。",
		InputSchema: obj(map[string]any{
			"node_id": str("节点 ID"),
			"cmd":     str("命令(经 /bin/sh -c)"),
			"timeout": intp("超时秒,默认 60,上限 300"),
		}, "node_id", "cmd"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID  string `json:"node_id"`
				Cmd     string `json:"cmd"`
				Timeout int    `json:"timeout"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Cmd == "" {
				return "", fmt.Errorf("cmd 不能为空")
			}
			if a.Timeout <= 0 {
				a.Timeout = 60
			}
			if a.Timeout > 300 {
				a.Timeout = 300
			}
			return d.RunExec(ctx, a.NodeID, a.Cmd, a.Timeout)
		},
	})

	// ---------- files ----------
	register(&Tool{
		Name: "file_list", Scope: ScopeFiles,
		Description: "列出节点目录条目(名称/类型/大小/权限)。",
		InputSchema: obj(map[string]any{
			"node_id": str("节点 ID"),
			"path":    str("目录绝对路径"),
		}, "node_id", "path"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID string `json:"node_id"`
				Path   string `json:"path"`
			}
			_ = json.Unmarshal(args, &a)
			return d.FileOp(ctx, a.NodeID, "list", a.Path, "", "")
		},
	})
	register(&Tool{
		Name: "file_read", Scope: ScopeFiles,
		Description: "读节点文本配置文件内容(白名单扩展名,≤512KB)。",
		InputSchema: obj(map[string]any{
			"node_id": str("节点 ID"),
			"path":    str("文件绝对路径"),
		}, "node_id", "path"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID string `json:"node_id"`
				Path   string `json:"path"`
			}
			_ = json.Unmarshal(args, &a)
			return d.FileOp(ctx, a.NodeID, "read", a.Path, "", "")
		},
	})
	register(&Tool{
		Name: "file_write", Scope: ScopeFiles,
		Description: "写节点文本文件(原子写,≤512KB)。修改配置文件用;高危。",
		InputSchema: obj(map[string]any{
			"node_id": str("节点 ID"),
			"path":    str("文件绝对路径"),
			"content": str("全文内容"),
		}, "node_id", "path", "content"),
		Handler: func(ctx context.Context, args json.RawMessage, d PanelDeps) (string, error) {
			var a struct {
				NodeID  string `json:"node_id"`
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			_ = json.Unmarshal(args, &a)
			return d.FileOp(ctx, a.NodeID, "write", a.Path, a.Content, "")
		},
	})
}

// sortedToolNames 供测试断言。
func sortedToolNames() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
