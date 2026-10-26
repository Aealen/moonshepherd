// Package executor 执行面板下发的指令（docs/01 §3.4、docs/04 §5.3）。
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aealen/moonshepherd/internal/probe"
	"github.com/aealen/moonshepherd/internal/protocol"
)

// E 执行器：无状态，可并发执行不同指令。
type E struct{}

// Result 执行产物（回执；probe 指令的 report 消息经 sender 直发）。
type Result struct {
	Reply protocol.CommandResultPayload
}

// Execute 分发执行；payload 为指令的 payload JSON。
func (e *E) Execute(ctx context.Context, cmdType string, payload json.RawMessage, timeoutS int, sender func(*protocol.Envelope)) Result {
	if timeoutS <= 0 {
		timeoutS = 120
	}
	switch cmdType {
	case protocol.CmdExec:
		return e.exec(ctx, payload, timeoutS)
	case protocol.CmdProbe:
		return e.probe(ctx, payload, sender)
	case protocol.CmdConfigApply:
		return e.configApply(ctx, payload, timeoutS)
	case protocol.CmdDockerOp:
		return e.dockerOp(ctx, payload, timeoutS)
	default:
		return fail(payload, fmt.Sprintf("不支持的指令类型: %s", cmdType))
	}
}

// exec 执行 shell 命令：/bin/sh -c，输出截断（docs/04 §5.3）。
func (e *E) exec(ctx context.Context, payload json.RawMessage, timeoutS int) Result {
	var p protocol.ExecPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.Cmd == "" {
		return fail(payload, "exec payload 非法：缺少 cmd")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", p.Cmd)
	var stdout, stderr limitBuf
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	SetSysProcAttr(cmd)
	err := cmd.Run()
	res := protocol.CommandResultPayload{Status: protocol.ResultSuccess, Stdout: stdout.String(), Stderr: stderr.String(), Truncated: stdout.truncated || stderr.truncated}
	if ctx.Err() == context.DeadlineExceeded {
		res.Status = protocol.ResultFailed
		res.Error = fmt.Sprintf("执行超时（%ds）", timeoutS)
		return Result{Reply: res}
	}
	if err != nil {
		res.Status = protocol.ResultFailed
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.Error = err.Error()
		}
	}
	return Result{Reply: res}
}

// probe 即时探测：每探测器一条 report 经 sender 附发，回执 success（docs/04 §5.3）。
func (e *E) probe(ctx context.Context, payload json.RawMessage, sender func(*protocol.Envelope)) Result {
	var p protocol.ProbePayload
	_ = json.Unmarshal(payload, &p)
	want := map[string]bool{}
	for _, n := range p.ProbeNames {
		want[n] = true
	}
	all := len(want) == 0
	for name, ctor := range probe.Registry {
		if !all && !want[name] {
			continue
		}
		apps, err := ctor().Detect(ctx)
		if err != nil {
			apps = []protocol.App{}
		}
		if sender != nil {
			sender(&protocol.Envelope{ID: newID(), Type: protocol.TypeReport, TS: time.Now().Unix(),
				Payload: protocol.ReportPayload{ProbeName: name, DetectedAt: time.Now().Unix(), Apps: apps}})
		}
	}
	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess}}
}

// ---------- config.apply：备份→写入→校验→重载→失败回滚（docs/01 §6） ----------

// appCmds 按应用注册的校验/重载命令模板，{path} 替换目标路径（docs/01 §6.2）。
type appCmds struct {
	Validate []string
	Reload   []string
}

var builtinAppCmds = map[string]appCmds{
	"nginx": {
		Validate: []string{"nginx", "-t"},
		Reload:   []string{"nginx", "-s", "reload"},
	},
	"caddy": {
		Validate: []string{"caddy", "validate", "--config", "{path}"},
		Reload:   []string{"sh", "-c", "systemctl reload caddy 2>/dev/null || caddy reload --config '{path}'"},
	},
}

const backupKeep = 10 // Agent 侧每目标保留最近 N 份备份（docs/01 §6.2）

func (e *E) configApply(ctx context.Context, payload json.RawMessage, timeoutS int) Result {
	var p protocol.ConfigApplyPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.TargetPath == "" {
		return fail(payload, "config.apply payload 非法")
	}
	ac, ok := builtinAppCmds[p.AppType]
	if !ok {
		return fail(payload, fmt.Sprintf("app_type %s 未注册 validate/reload，拒绝下发", p.AppType))
	}
	stage := func(stages []protocol.Stage, name, status, detail string) []protocol.Stage {
		return append(stages, protocol.Stage{Name: name, Status: status, Detail: detail})
	}
	runCmd := func(argv []string) (string, error) {
		ctxT, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		final := make([]string, len(argv))
		for i, a := range argv {
			final[i] = strings.ReplaceAll(a, "{path}", p.TargetPath)
		}
		out, err := exec.CommandContext(ctxT, final[0], final[1:]...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	stages := []protocol.Stage{}
	failed := func(stageName, detail string) Result {
		return Result{Reply: protocol.CommandResultPayload{
			Status: protocol.ResultFailed, Error: fmt.Sprintf("%s 阶段失败", stageName), Stages: stages,
		}}
	}

	// ① 备份
	backupPath := ""
	if _, err := os.Stat(p.TargetPath); err == nil {
		dir, base := filepath.Split(p.TargetPath)
		backupPath = filepath.Join(dir, fmt.Sprintf(".%s.herd-bak-%d", base, time.Now().Unix()))
		if err := copyFile(p.TargetPath, backupPath); err != nil {
			stages = stage(stages, "backup", "failed", err.Error())
			return failed("backup", err.Error())
		}
		pruneBackups(filepath.Join(dir, "."+base+".herd-bak-*"))
		stages = stage(stages, "backup", "success", backupPath)
	} else {
		stages = stage(stages, "backup", "skipped", "目标文件不存在（首次下发）")
	}

	// ② 原子写入：临时文件 + rename（docs/01 §6.2）
	dir := filepath.Dir(p.TargetPath)
	tmp, err := os.CreateTemp(dir, ".herd-tmp-*")
	if err != nil {
		stages = stage(stages, "write", "failed", err.Error())
		return failed("write", err.Error())
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(p.Content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		stages = stage(stages, "write", "failed", err.Error())
		return failed("write", err.Error())
	}
	tmp.Close()
	_ = os.Chmod(tmpName, 0o644)
	if err := os.Rename(tmpName, p.TargetPath); err != nil {
		os.Remove(tmpName)
		stages = stage(stages, "write", "failed", err.Error())
		return failed("write", err.Error())
	}
	stages = stage(stages, "write", "success", "")

	rollback := func(reason string) Result {
		if backupPath != "" {
			if err := copyFile(backupPath, p.TargetPath); err != nil {
				stages = stage(stages, "rollback", "failed", err.Error())
			} else {
				if out, err := runCmd(ac.Reload); err != nil {
					stages = stage(stages, "rollback", "failed", "恢复备份成功但重载旧配置失败: "+out)
				} else {
					stages = stage(stages, "rollback", "success", "已恢复备份并重载旧配置")
				}
			}
		} else {
			// 首次下发无备份：移除新文件
			os.Remove(p.TargetPath)
			stages = stage(stages, "rollback", "success", "首次下发失败，已移除新文件")
		}
		return Result{Reply: protocol.CommandResultPayload{
			Status: protocol.ResultFailed, Error: reason, Stages: stages,
		}}
	}

	// ③ 本地校验（docs/01 §6：校验失败自动回滚）
	if out, err := runCmd(ac.Validate); err != nil {
		stages = stage(stages, "validate", "failed", out)
		return rollback("配置校验失败: " + firstLine(out))
	}
	stages = stage(stages, "validate", "success", "")

	// ④ 重载
	if out, err := runCmd(ac.Reload); err != nil {
		stages = stage(stages, "reload", "failed", out)
		return rollback("重载失败: " + firstLine(out))
	}
	stages = stage(stages, "reload", "success", "")

	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess, Stages: stages}}
}

// ---------- 辅助 ----------

func fail(payload json.RawMessage, msg string) Result {
	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultFailed, Error: msg}}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// pruneBackups 只保留最近 backupKeep 份。
func pruneBackups(pattern string) {
	matches, _ := filepath.Glob(pattern)
	if len(matches) <= backupKeep {
		return
	}
	// 文件名含 Unix 时间戳，字典序即时间序
	for _, m := range matches[:len(matches)-backupKeep] {
		_ = os.Remove(m)
	}
}

// limitBuf 截断缓冲：1 MiB 上限（docs/04 §5.3）。
type limitBuf struct {
	buf       []byte
	truncated bool
}

func (b *limitBuf) Write(p []byte) (int, error) {
	const max = protocol.MaxPayloadBytes
	if len(b.buf) < max {
		room := max - len(b.buf)
		if len(p) <= room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitBuf) String() string { return string(b.buf) }

func newID() string {
	return fmt.Sprintf("m_%d", time.Now().UnixNano())
}
