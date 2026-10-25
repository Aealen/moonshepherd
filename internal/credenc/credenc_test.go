package credenc

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	dir := t.TempDir()
	box, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("sec_0123456789abcdef")
	enc, err := box.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	if enc == string(secret) {
		t.Fatal("密文不得等于明文")
	}
	got, err := box.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("roundtrip 失败: %q != %q", got, secret)
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	b1, _ := Open(dir1)
	b2, _ := Open(dir2)
	enc, err := b1.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b2.Decrypt(enc); err == nil {
		t.Fatal("不同 master.key 必须解不开（AES-GCM 认证失败）")
	}
}

func TestMasterKeyGeneratedOnce(t *testing.T) {
	dir := t.TempDir()
	b1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := b1.Encrypt([]byte("x"))
	if _, err := b2.Decrypt(enc); err != nil {
		t.Fatal("同一目录二次 Open 应复用 master.key")
	}
	// master.key 权限（Windows 上 chmod 语义有限，跳过权限断言）
	if _, err := os.Stat(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal("master.key 应存在")
	}
}
