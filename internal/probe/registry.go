// Package probe 探测器插件：每种应用一个探测器（docs/01 §3.3）。
// 新增应用支持 = 新增一个探测器文件 + 注册，不动核心。
package probe

import (
	"context"

	"github.com/maxonphong/herd/internal/protocol"
)

// Probe 探测器统一接口（docs/01 §3.3）。
type Probe interface {
	Name() string
	Detect(ctx context.Context) ([]protocol.App, error)
}

// Registry 已注册探测器（名字 → 构造函数）。
var Registry = map[string]func() Probe{}

// Register 注册探测器（各探测器文件的 init 调用）。
func Register(name string, ctor func() Probe) {
	Registry[name] = ctor
}

// Available 当前平台可用的探测器名列表（上报 capabilities 用）。
func Available() []string {
	names := make([]string, 0, len(Registry))
	for name := range Registry {
		names = append(names, name)
	}
	return names
}

// DetectAll 全量探测；单个探测器失败返回该探测器的空结果 + unknown，不影响其他（docs/01 §3.3：探不出明确上报，不臆造）。
func DetectAll(ctx context.Context) map[string][]protocol.App {
	out := map[string][]protocol.App{}
	for name, ctor := range Registry {
		p := ctor()
		apps, err := p.Detect(ctx)
		if err != nil || apps == nil {
			out[name] = []protocol.App{}
			continue
		}
		out[name] = apps
	}
	return out
}
