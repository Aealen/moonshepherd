package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// 跨平台伪命令：validate 总是失败 / reload 总是成功。
func fakeCmds(failValidate bool) appCmds {
	if runtime.GOOS == "windows" {
		v := []string{"cmd", "/c", "exit", "0"}
		if failValidate {
			v = []string{"cmd", "/c", "exit", "1"}
		}
		return appCmds{Validate: v, Reload: []string{"cmd", "/c", "exit", "0"}}
	}
	v := []string{"true"}
	if failValidate {
		v = []string{"false"}
	}
	return appCmds{Validate: v, Reload: []string{"true"}}
}

// M4 验收预演：校验失败 → 备份自动恢复（docs/03 §5 M4、docs/01 §6.1）。
func TestConfigApplyValidateFailRollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.conf")
	oldContent := "# 原配置\nserver {\n}\n"
	if err := os.WriteFile(target, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}
	old := builtinAppCmds["nginx"]
	builtinAppCmds["nginx"] = fakeCmds(true) // validate 必失败
	defer func() { builtinAppCmds["nginx"] = old }()

	e := &E{}
	payload, _ := json.Marshal(protocol.ConfigApplyPayload{
		AppType: "nginx", TargetPath: target, Content: "# 坏配置", VersionID: 1,
	})
	res := e.Execute(context.Background(), protocol.CmdConfigApply, payload, 30, nil)

	if res.Reply.Status != protocol.ResultFailed {
		t.Fatalf("校验失败应回 failed，实得 %s", res.Reply.Status)
	}
	// stages：backup success → write success → validate failed → rollback success
	var stages []protocol.Stage
	_ = json.Unmarshal(mustJSON(res.Reply.Stages), &stages)
	if len(stages) != 4 {
		t.Fatalf("应有 4 个阶段，实得 %d: %+v", len(stages), stages)
	}
	if stages[2].Name != "validate" || stages[2].Status != "failed" {
		t.Fatalf("validate 阶段应 failed: %+v", stages)
	}
	if stages[3].Name != "rollback" || stages[3].Status != "success" {
		t.Fatalf("rollback 阶段应 success: %+v", stages)
	}
	// 文件内容必须恢复为备份
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != oldContent {
		t.Fatalf("目标文件应恢复原内容，实得 %q", got)
	}
}

// 正常路径：校验通过 → 新内容生效。
func TestConfigApplySuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.conf")
	old := builtinAppCmds["nginx"]
	builtinAppCmds["nginx"] = fakeCmds(false)
	defer func() { builtinAppCmds["nginx"] = old }()

	e := &E{}
	payload, _ := json.Marshal(protocol.ConfigApplyPayload{
		AppType: "nginx", TargetPath: target, Content: "# 新配置", VersionID: 1,
	})
	res := e.Execute(context.Background(), protocol.CmdConfigApply, payload, 30, nil)
	if res.Reply.Status != protocol.ResultSuccess {
		t.Fatalf("应 success，实得 %s（stages=%+v）", res.Reply.Status, res.Reply.Stages)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "# 新配置" {
		t.Fatalf("文件应写入新内容，实得 %q", got)
	}
	// 不留临时文件
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if filepath.Base(en.Name()) != "app.conf" {
			t.Fatalf("残留临时/备份文件: %s", en.Name())
		}
	}
}

// 未知 app_type 必须拒绝下发（docs/01 §6.2）。
func TestConfigApplyRejectsUnknownApp(t *testing.T) {
	e := &E{}
	payload, _ := json.Marshal(protocol.ConfigApplyPayload{AppType: "apache", TargetPath: "/tmp/x", Content: "x"})
	res := e.Execute(context.Background(), protocol.CmdConfigApply, payload, 30, nil)
	if res.Reply.Status != protocol.ResultFailed || res.Reply.Error == "" {
		t.Fatalf("未知 app_type 应拒绝: %+v", res.Reply)
	}
}

// 输出截断：超 1MiB 截断并标记（docs/04 §5.3）。
func TestExecOutputTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec 依赖 /bin/sh，跳过 Windows")
	}
	e := &E{}
	// 生成 2MiB 输出
	payload, _ := json.Marshal(protocol.ExecPayload{Cmd: "head -c 2097152 /dev/zero | tr '\\0' 'x'"})
	res := e.Execute(context.Background(), protocol.CmdExec, payload, 30, nil)
	if !res.Reply.Truncated {
		t.Fatal("超限输出应标记 truncated")
	}
	if len(res.Reply.Stdout) > protocol.MaxPayloadBytes {
		t.Fatalf("stdout 应截断至 1MiB，实得 %d", len(res.Reply.Stdout))
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
