// 远端文件只读查看(docs/04 §10)。
// 安全闸:扩展名白名单(文本配置)+ 512KB 截断 + 二进制探测(NUL)拒绝;
// 内容只回不落库;请求落面板侧审计(file.read)。
package agentd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// fileReadExts 可查看的文本配置扩展名(小写含点;"" = 无扩展名)。
var fileReadExts = map[string]bool{
	".conf": true, ".cfg": true, ".cnf": true, ".ini": true, ".toml": true,
	".yml": true, ".yaml": true, ".json": true, ".xml": true, ".properties": true,
	".env": true, ".list": true, ".sh": true, ".service": true, ".caddyfile": true,
	"": true, // Caddyfile 等无扩展名文本
}

// readFile 读取并校验;err 非 nil 时回 error。
func readFile(path string) (content string, size int64, truncated bool, err error) {
	if path == "" || !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return "", 0, false, errFileRejected("路径须为绝对路径且不得包含 ..")
	}
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)
	// Caddyfile / *.env 等无扩展名或有专属扩展名才放行;.txt/.log/.bin 等一律拒
	if !fileReadExts[ext] && !isKnownConfName(base) {
		return "", 0, false, errFileRejected("仅支持文本配置文件(conf/yml/toml/ini/json/env/service 等)")
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, false, errFileRejected("文件不存在或不可访问")
	}
	if st.IsDir() {
		return "", 0, false, errFileRejected("目标是目录")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, false, errFileRejected("打开失败(权限?)")
	}
	defer f.Close()

	limited := io.LimitReader(f, protocol.MaxFileReadBytes+1)
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(limited); err != nil {
		return "", 0, false, errFileRejected("读取失败")
	}
	data := buf.Bytes()
	truncated = int64(len(data)) > protocol.MaxFileReadBytes
	if truncated {
		data = data[:protocol.MaxFileReadBytes]
	}
	// 二进制探测:首 8KB 含 NUL 即拒
	if i := bytes.IndexByte(data[:min(len(data), 8192)], 0); i >= 0 {
		return "", 0, false, errFileRejected("疑似二进制文件,拒绝查看")
	}
	return string(data), st.Size(), truncated, nil
}

// isKnownConfName 无扩展名但公认的配置文件名。
func isKnownConfName(base string) bool {
	switch strings.ToLower(base) {
	case "caddyfile", "dockerfile", ".env", ".editorconfig", "hosts", "passwd", "group":
		return true
	}
	return false
}

type fileRejectError string

func (e fileRejectError) Error() string { return string(e) }

func errFileRejected(msg string) error { return fileRejectError(msg) }

// handleFileRead 面板消息入口:读 → 经 sender 回 ack。
func handleFileRead(env protocol.Envelope, sender func(*protocol.Envelope)) {
	var p protocol.FileReadPayload
	b, _ := json.Marshal(env.Payload)
	if json.Unmarshal(b, &p) != nil || p.RequestID == "" || p.Path == "" {
		return
	}
	content, size, truncated, err := readFile(p.Path)
	ack := protocol.FileReadAckPayload{RequestID: p.RequestID, Content: content, Size: size, Truncated: truncated}
	if err != nil {
		ack.Error = err.Error()
		ack.Content = ""
	}
	sender(&protocol.Envelope{ID: newID(), Type: protocol.TypeFileReadAck, TS: time.Now().Unix(), Payload: ack})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
