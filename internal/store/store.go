// Package store 是面板侧 SQLite 访问层（docs/01 §7）。
// Agent 永不引用本包（docs/02 §2 结构原则：store 只在面板侧使用）。
package store

import (
	"database/sql"
	_ "embed" // embed 驱动 schema.sql
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，无 CGO（ADR-4 联动决策）
)

//go:embed schema.sql
var schemaSQL string

// Store 封装 *sql.DB，所有方法并发安全（database/sql 内部连接池）。
type Store struct {
	DB *sql.DB
}

// Open 打开/创建 dataDir/herd.db 并执行迁移。WAL 模式提升并发读写性能。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "herd.db")) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：限制单连接避免 SQLITE_BUSY 抖动；读写共用（WAL 下读不阻塞）
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("执行 schema 迁移: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func now() int64 { return time.Now().Unix() }
