// Package credenc 实现节点凭据的 AES-256-GCM 加密落库（docs/05 §3.4）。
// 加密密钥为 data 目录下 master.key（首次启动 crypto/rand 生成，权限 0600）。
package credenc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const masterKeySize = 32 // AES-256

// Box 持有 master key，提供 Encrypt/Decrypt。
type Box struct {
	key []byte
}

// Open 加载 dataDir/master.key；不存在则生成（0600）。
func Open(dataDir string) (*Box, error) {
	path := filepath.Join(dataDir, "master.key")
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != masterKeySize {
			return nil, fmt.Errorf("master.key 长度异常（%d 字节，应为 %d），文件可能损坏", len(key), masterKeySize)
		}
		return &Box{key: key}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 master.key: %w", err)
	}
	key = make([]byte, masterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成 master.key: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("写入 master.key: %w", err)
	}
	return &Box{key: key}, nil
}

// Encrypt 返回 "enc1:" 前缀 + nonce + ciphertext 的整体 base64（带版本前缀便于未来算法演进）。
func (b *Box) Encrypt(plaintext []byte) (string, error) {
	aead, err := b.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// 密文格式：nonce || ciphertext，整体 hex，加 "enc1:" 版本前缀
	sealed := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return "enc1:" + hexEncode(out), nil
}

// Decrypt 解开 Encrypt 的产物。
func (b *Box) Decrypt(s string) ([]byte, error) {
	const prefix = "enc1:"
	if len(s) < len(prefix) || s[:len(prefix)] != prefix {
		return nil, errors.New("凭据格式不识别（缺少 enc1 前缀）")
	}
	raw, err := hexDecode(s[len(prefix):])
	if err != nil {
		return nil, err
	}
	aead, err := b.aead()
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("凭据密文长度异常")
	}
	return aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
}

func (b *Box) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, hextable[v>>4], hextable[v&0x0f])
	}
	return string(out)
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("hex 长度非偶数")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, err := hexNibble(s[i*2])
		if err != nil {
			return nil, err
		}
		lo, err := hexNibble(s[i*2+1])
		if err != nil {
			return nil, err
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("非法 hex 字符 %q", c)
}
