//go:build !linux

package agentd

import "github.com/maxonphong/herd/internal/protocol"

// collectHeartbeat 非 Linux 平台（Windows/macOS 开发机调试）：返回空快照。
// 字段全部省略，面板按 docs/04 §5.1 容缺处理（last_seen 照常更新）。
func collectHeartbeat() protocol.HeartbeatPayload {
	return protocol.HeartbeatPayload{}
}
