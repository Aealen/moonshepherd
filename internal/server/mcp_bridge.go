// MCP PanelDeps 实现(server 包内桥接;docs/07a §4)。
// 同步等待:指令类轮询 store 等回执;file.* 经 fileBridge 等 WSS ack。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/internal/store"
)

// mcpDeps PanelDeps 面板实现。
type mcpDeps struct{ s *Server }

func (m mcpDeps) Store() *store.Store { return m.s.st }

func (m mcpDeps) Audit(action, target, detailJSON, ip, keyName string) {
	m.s.st.AddAudit(context.Background(), 0, "mcp:"+keyName, action, target, detailJSON, ip)
}

func (m mcpDeps) NodeOnline(nodeID string) bool { return m.s.hub.online(nodeID) }

func (m mcpDeps) PanelInfo(ctx context.Context) (string, error) {
	nodes, _, _ := m.s.st.ListNodes(ctx, store.NodeFilter{}, 1, 100)
	online := 0
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}
	return fmt.Sprintf("MoonShepherd 牧云面板 · 节点在线 %d/%d", online, len(nodes)), nil
}

func (m mcpDeps) ListConfigsText(ctx context.Context, nodeID string) (string, error) {
	targets, err := m.s.st.ListConfigTargets(ctx, nodeID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个配置目标:\n", len(targets))
	for _, t := range targets {
		fmt.Fprintf(&b, "- #%d [%s] %s (当前版本 v%d, %s)\n", t.ID, t.AppType, t.TargetPath, t.CurrentVersionID, orDash(t.LastApplyStatus))
	}
	return b.String(), nil
}

func orDash(s string) string {
	if s == "" {
		return "未下发"
	}
	return s
}

func (m mcpDeps) GetConfigText(ctx context.Context, targetID, version int64) (string, error) {
	t, err := m.s.st.GetConfigTargetByID(ctx, targetID)
	if err != nil || t == nil {
		return "", fmt.Errorf("配置目标不存在")
	}
	var v *store.ConfigVersion
	if version > 0 {
		if v, err = m.s.st.GetConfigVersion(ctx, version); err != nil || v == nil || v.TargetID != targetID {
			return "", fmt.Errorf("版本不存在")
		}
	} else {
		vs, err := m.s.st.ListConfigVersions(ctx, targetID)
		if err != nil || len(vs) == 0 {
			return "", fmt.Errorf("目标暂无版本")
		}
		v = &vs[0] // 最新
	}
	return fmt.Sprintf("# %s v%d (%s)\n%s", t.TargetPath, v.ID, v.Status, v.Content), nil
}

// RunDockerOp 容器操作(含 logs)。
func (m mcpDeps) RunDockerOp(ctx context.Context, nodeID, op, container string, tail int) (string, error) {
	payload := protocol.DockerOpPayload{Op: op, Container: container, TailLines: tail}
	return m.runCommand(ctx, nodeID, protocol.CmdDockerOp, payload, 60)
}

func (m mcpDeps) RunExec(ctx context.Context, nodeID, cmd string, timeoutS int) (string, error) {
	return m.runCommand(ctx, nodeID, protocol.CmdExec, protocol.ExecPayload{Cmd: cmd}, timeoutS)
}

// FileOp 文件操作:file.read / file.op 下发 + fileBridge 等 ack。
func (m mcpDeps) FileOp(ctx context.Context, nodeID, op, path, content, dest string) (string, error) {
	switch op {
	case "read":
		rid, err := m.s.fileBridge.sendRead(nodeID, path)
		if err != nil {
			return "", err
		}
		ackAny := m.s.fileBridge.wait(ctx, rid, 65*time.Second)
		if ackAny == nil {
			return "", fmt.Errorf("节点响应超时")
		}
		b, _ := json.Marshal(ackAny)
		var p protocol.FileReadAckPayload
		if json.Unmarshal(b, &p) != nil || p.Error != "" {
			return "", fmt.Errorf("%s", firstNonEmpty(p.Error, "读取失败"))
		}
		out := p.Content
		if p.Truncated {
			out += "\n…(超出 512KB 已截断)"
		}
		return out, nil
	case "list", "write":
		payload := protocol.FileOpPayload{Op: op, Path: path, Content: content, Dest: dest}
		rid, err := m.s.fileBridge.sendOp(nodeID, payload)
		if err != nil {
			return "", err
		}
		ackAny := m.s.fileBridge.wait(ctx, rid, 65*time.Second)
		if ackAny == nil {
			return "", fmt.Errorf("节点响应超时")
		}
		b, _ := json.Marshal(ackAny)
		var p protocol.FileOpAckPayload
		if json.Unmarshal(b, &p) != nil {
			return "", fmt.Errorf("应答解析失败")
		}
		if p.Error != "" {
			return "", fmt.Errorf("%s", p.Error)
		}
		if op == "list" {
			var b2 strings.Builder
			for _, e := range p.Entries {
				tag := "f"
				if e.IsDir {
					tag = "d"
				}
				fmt.Fprintf(&b2, "%s %8d %s %s\n", e.Mode, e.Size, tag, e.Name)
			}
			return b2.String(), nil
		}
		return "已保存(原子写入)", nil
	}
	return "", fmt.Errorf("未知 op")
}

// runCommand 指令同步等待:创建 → 轮询回执。
// createCommandFor 需要 *http.Request(取 session 用户名);MCP 侧用户名
// 已是 mcp:xxx,这里用哑 Request 携带(调用方 ctx 透传超时)。
func (m mcpDeps) runCommand(ctx context.Context, nodeID, cmdType string, payload any, timeoutS int) (string, error) {
	req := httptest.NewRequest("POST", "/mcp-internal", nil).WithContext(ctx)
	cmd, err := m.s.createCommandFor(req, nodeID, cmdType, payload, timeoutS+5, "")
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
		c, err := m.s.st.GetCommand(context.Background(), cmd.ID)
		if err != nil || c == nil {
			continue
		}
		switch c.Status {
		case "success":
			out := c.Stdout
			if c.Stderr != "" {
				out += "\n[stderr]\n" + c.Stderr
			}
			if c.ExitCode != nil && *c.ExitCode != 0 {
				out += fmt.Sprintf("\n(exit_code=%d)", c.ExitCode)
			}
			return strings.TrimSpace(out), nil
		case "failed":
			msg := c.Error
			if msg == "" {
				msg = c.Stderr
			}
			return "", fmt.Errorf("执行失败: %s", msg)
		case "lost":
			return "", fmt.Errorf("节点连接中断,结果未知")
		}
	}
	return "", fmt.Errorf("执行超时(%ds)", timeoutS)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
