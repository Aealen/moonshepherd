// 文件管理器 Agent 侧(docs/04 §11):目录树/常用操作/上传组装。
//
// 权限兼容:所有操作 EACCES/EPERM → denied 回执(前端提示"权限不足"),
// 目录读取失败的子项 → NoPerm 占位(树不塌);ENOENT → 明确"不存在"。
// 安全:虚拟文件系统(/proc /sys /dev /run)拒变更;写一律临时文件+rename;
// 删除非递归优先,递归需显式 recursive。
package agentd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// fileOps 文件操作入口(消息分发调用)。
func handleFileOp(env protocol.Envelope, sender func(*protocol.Envelope)) {
	var p protocol.FileOpPayload
	b, _ := json.Marshal(env.Payload)
	if json.Unmarshal(b, &p) != nil || p.RequestID == "" {
		return
	}
	ack := protocol.FileOpAckPayload{RequestID: p.RequestID, OK: true}
	switch p.Op {
	case "list":
		entries, denied, err := listDir(p.Path)
		if err != nil {
			ack = opErr(p.RequestID, err)
		} else {
			ack.Entries = entries
			_ = denied // 目录本身的 denied 已含在 err 分支;子项占位见 listDir
		}
	case "write":
		if err := writeText(p.Path, p.Content); err != nil {
			ack = opErr(p.RequestID, err)
		}
	case "mkdir":
		if err := os.MkdirAll(p.Path, 0o755); err != nil {
			ack = opErr(p.RequestID, err)
		}
	case "delete":
		if err := deletePath(p.Path, p.Recursive); err != nil {
			ack = opErr(p.RequestID, err)
		}
	case "move", "rename":
		if err := guardVirtualFS(p.Path); err != nil {
			ack = opErr(p.RequestID, err)
		} else if err := os.Rename(p.Path, p.Dest); err != nil {
			ack = opErr(p.RequestID, err)
		}
	case "copy":
		if err := copyPath(p.Path, p.Dest); err != nil {
			ack = opErr(p.RequestID, err)
		}
	default:
		ack = protocol.FileOpAckPayload{RequestID: p.RequestID, Error: "未知 op: " + p.Op}
	}
	sender(&protocol.Envelope{ID: newID(), Type: protocol.TypeFileOpAck, TS: nowUnix(), Payload: ack})
}

// opErr 统一错误分类:权限类 → denied=true。
func opErr(reqID string, err error) protocol.FileOpAckPayload {
	msg := err.Error()
	denied := strings.Contains(msg, "permission denied") || strings.Contains(msg, "not permitted") ||
		strings.Contains(msg, "read-only file system")
	hint := ""
	if denied {
		hint = "(权限不足:Agent 进程身份对该路径无权;可检查属主/ACL)"
	}
	return protocol.FileOpAckPayload{RequestID: reqID, Error: msg + hint, Denied: denied}
}

// listDir 目录列表:权限失败的条目 → NoPerm 占位;排序目录优先。
func listDir(dir string) ([]protocol.FileEntry, bool, error) {
	if dir == "" || !strings.HasPrefix(dir, "/") {
		return nil, false, fmt.Errorf("路径须为绝对路径")
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, isPermErr(err), err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, isPermErr(err), err
	}
	sort.Strings(names)
	entries := make([]protocol.FileEntry, 0, len(names))
	for _, name := range names {
		full := filepath.Join(dir, name)
		st, err := os.Lstat(full)
		if err != nil {
			// 读不到 stat(权限) → 占位,不塌树
			entries = append(entries, protocol.FileEntry{Name: name, NoPerm: true})
			continue
		}
		e := protocol.FileEntry{Name: name, IsDir: st.IsDir(), Size: st.Size(), ModTime: st.ModTime().Unix()}
		e.Mode = st.Mode().String()
		entries = append(entries, e)
	}
	// 目录排前
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, false, nil
}

// writeText 原子写:临时文件 + rename(编辑保存路径,≤512KB 文本)。
func writeText(path, content string) error {
	if err := guardVirtualFS(path); err != nil {
		return err
	}
	if len(content) > protocol.MaxFileWriteBytes {
		return fmt.Errorf("内容超 %dKB 上限(大文件请用上传)", protocol.MaxFileWriteBytes>>10)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".herd-fm-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// deletePath 删除:目录默认须空,recursive 显式递归;虚拟 fs 拒绝。
func deletePath(path string, recursive bool) error {
	if err := guardVirtualFS(path); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return os.Remove(path)
	}
	if recursive {
		return os.RemoveAll(path)
	}
	return os.Remove(path) // 目录非空 → err("directory not empty"),前端提示改递归
}

// copyPath 复制:文件直接拷;目录递归拷(MkdirAll+walk)。
func copyPath(src, dst string) error {
	if err := guardVirtualFS(dst); err != nil {
		return err
	}
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFileLocal(src, dst)
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFileLocal(p, target)
	})
}

func copyFileLocal(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// guardVirtualFS 虚拟/系统文件系统拒变更(浏览允许,写删移拒)。
func guardVirtualFS(path string) error {
	for _, p := range []string{"/proc", "/sys", "/dev", "/run"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return fmt.Errorf("系统虚拟文件系统(%s)禁止变更", p)
		}
	}
	if path == "/" {
		return fmt.Errorf("拒绝操作根目录")
	}
	return nil
}

func isPermErr(err error) bool {
	return err != nil && (os.IsPermission(err) || strings.Contains(err.Error(), "not permitted"))
}

// ---------- 上传组装(分片累积 → done 校验就位) ----------

type fileUploadSession struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	seq     int
	size    int64
	created time.Time
}

var fileUploads sync.Map // session_id → *fileUploadSession

const (
	maxUploadSessions = 8                // 并发上传会话上限
	uploadSessionTTL  = 10 * time.Minute // 单会话最长存活(防遗忘占盘)
)

func init() { go uploadSessionSweeper() }

// uploadSessionSweeper 周期清理超时会话(临时文件一并删除)。
func uploadSessionSweeper() {
	for range time.Tick(time.Minute) {
		fileUploads.Range(func(k, v any) bool {
			s := v.(*fileUploadSession)
			if time.Since(s.created) > uploadSessionTTL {
				fileUploads.Delete(k)
				s.mu.Lock()
				s.f.Close()
				os.Remove(s.f.Name())
				s.mu.Unlock()
			}
			return true
		})
	}
}

// handleUploadChunk 分片顺序写入(.part 临时文件)。
func handleUploadChunk(env protocol.Envelope) {
	var p protocol.FileUploadChunkPayload
	b, _ := json.Marshal(env.Payload)
	if json.Unmarshal(b, &p) != nil || p.SessionID == "" {
		return
	}
	sAny, ok := fileUploads.Load(p.SessionID)
	if !ok {
		return
	}
	s := sAny.(*fileUploadSession)
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil || p.Seq != s.seq || s.size+int64(len(data)) > protocol.MaxFileUploadBytes {
		return // 乱序/损坏静默丢:done 时 sha256 必然不过
	}
	if _, err := s.f.Write(data); err == nil {
		s.seq++
		s.size += int64(len(data))
	}
}

// ensureUploadSession 按首片建会话(上限保护:超并发拒新)。
func ensureUploadSession(sessionID, finalPath string) (*fileUploadSession, error) {
	if v, ok := fileUploads.Load(sessionID); ok {
		return v.(*fileUploadSession), nil
	}
	if err := guardVirtualFS(finalPath); err != nil {
		return nil, err
	}
	n := 0
	fileUploads.Range(func(_, _ any) bool { n++; return n <= maxUploadSessions })
	if n >= maxUploadSessions {
		return nil, fmt.Errorf("并发上传会话已达上限 %d", maxUploadSessions)
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(finalPath), ".herd-upload-*")
	if err != nil {
		return nil, err
	}
	s := &fileUploadSession{path: finalPath, f: f, created: time.Now()}
	actual, _ := fileUploads.LoadOrStore(sessionID, s)
	return actual.(*fileUploadSession), nil
}

// handleUploadDone 校验 sha256 → 原子就位 → 回执。
// 顺序:Close → 校验 → chmod → rename;失败路径才清理临时文件。
func handleUploadDone(env protocol.Envelope, sender func(*protocol.Envelope)) {
	var p protocol.FileUploadDonePayload
	b, _ := json.Marshal(env.Payload)
	if json.Unmarshal(b, &p) != nil || p.SessionID == "" {
		return
	}
	ack := protocol.FileOpAckPayload{RequestID: p.SessionID, OK: true}
	sAny, ok := fileUploads.Load(p.SessionID)
	if !ok {
		ack = protocol.FileOpAckPayload{RequestID: p.SessionID, Error: "上传会话不存在或已过期"}
		sender(&protocol.Envelope{ID: newID(), Type: protocol.TypeFileOpAck, TS: nowUnix(), Payload: ack})
		return
	}
	s := sAny.(*fileUploadSession)
	fileUploads.Delete(p.SessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Close()

	cleanup := func() { _ = os.Remove(s.f.Name()) }
	switch {
	case p.Size > protocol.MaxFileUploadBytes:
		cleanup()
		ack = opErr(p.SessionID, fmt.Errorf("文件超 %dMB 上限", protocol.MaxFileUploadBytes>>20))
	case s.size != p.Size:
		cleanup()
		ack = protocol.FileOpAckPayload{RequestID: p.SessionID,
			Error: fmt.Sprintf("分片不完整:期望 %d 字节实收 %d,已丢弃(请重试)", p.Size, s.size)}
	default:
		h, err := fileSHA256(s.f.Name())
		if err != nil || h != strings.ToLower(p.SHA256) {
			cleanup()
			ack = protocol.FileOpAckPayload{RequestID: p.SessionID, Error: "sha256 校验失败,已丢弃(请重试)"}
		} else if err := os.Chmod(s.f.Name(), 0o644); err != nil {
			cleanup()
			ack = opErr(p.SessionID, err)
		} else if err := os.Rename(s.f.Name(), s.path); err != nil {
			cleanup()
			ack = opErr(p.SessionID, err)
		}
	}
	sender(&protocol.Envelope{ID: newID(), Type: protocol.TypeFileOpAck, TS: nowUnix(), Payload: ack})
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func nowUnix() int64 { return time.Now().Unix() }
