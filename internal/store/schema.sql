-- 牧云 Herd SQLite schema（docs/01 §7 + docs/04 §2.2 + docs/06 §2.5）
-- 约定：所有时间戳为 Unix 秒；JSON 字段以 TEXT 存储。

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    totp_secret   TEXT,
    role          TEXT NOT NULL DEFAULT 'admin',
    created_at    INTEGER NOT NULL
);

-- 节点表：credential_enc 为 AES-256-GCM 加密的 node_secret（docs/05 §3.4）
CREATE TABLE IF NOT EXISTS nodes (
    id             TEXT PRIMARY KEY,          -- n_ 前缀 UUID
    name           TEXT NOT NULL,
    agent_version  TEXT NOT NULL DEFAULT '',
    proto_version  TEXT NOT NULL DEFAULT '',
    os_name        TEXT NOT NULL DEFAULT '',
    os_version     TEXT NOT NULL DEFAULT '',
    os_arch        TEXT NOT NULL DEFAULT '',
    hostname       TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'offline', -- online/offline/pending→注册成功即 online
    enabled        INTEGER NOT NULL DEFAULT 1,
    last_seen      INTEGER NOT NULL DEFAULT 0,
    tags           TEXT NOT NULL DEFAULT '[]',
    capabilities   TEXT NOT NULL DEFAULT '[]',
    credential_enc TEXT NOT NULL,
    created_at     INTEGER NOT NULL
);

-- 一次性注册 Token（只存哈希，docs/04 §2.2）
CREATE TABLE IF NOT EXISTS registration_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    note       TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'active', -- active/used/revoked
    node_id    TEXT,                           -- 使用后回填
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

-- 应用清单（每次全量探测覆盖式更新该节点该探测器行）
CREATE TABLE IF NOT EXISTS app_inventory (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id     TEXT NOT NULL,
    probe_name  TEXT NOT NULL,
    app_name    TEXT NOT NULL,
    app_type    TEXT NOT NULL,
    version     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'unknown',
    detail      TEXT NOT NULL DEFAULT '{}',
    detected_at INTEGER NOT NULL,
    UNIQUE (node_id, probe_name, app_name)
);
CREATE INDEX IF NOT EXISTS idx_app_node ON app_inventory (node_id);
CREATE INDEX IF NOT EXISTS idx_app_type ON app_inventory (app_type);

-- 指令审计（不可删除）
CREATE TABLE IF NOT EXISTS commands (
    id          TEXT PRIMARY KEY,             -- c_ 前缀 UUID
    node_id     TEXT NOT NULL,
    parent_id   TEXT NOT NULL DEFAULT '',     -- 批量下发组 ID(同批次子指令共享;单发为空串)
    type        TEXT NOT NULL,
    payload     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending', -- pending/sent/running/success/failed/expired/lost
    stdout      TEXT NOT NULL DEFAULT '',
    stderr      TEXT NOT NULL DEFAULT '',
    truncated   INTEGER NOT NULL DEFAULT 0,
    exit_code   INTEGER,
    stages      TEXT NOT NULL DEFAULT '[]',
    error       TEXT NOT NULL DEFAULT '',
    timeout_s   INTEGER NOT NULL DEFAULT 120,
    created_by  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    sent_at     INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cmd_node ON commands (node_id, created_at);
CREATE INDEX IF NOT EXISTS idx_cmd_parent ON commands (parent_id) WHERE parent_id != '';
CREATE INDEX IF NOT EXISTS idx_cmd_status ON commands (status);

-- 配置目标：node × app_type × target_path（docs/06 §2.5）
CREATE TABLE IF NOT EXISTS config_targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id     TEXT NOT NULL,
    app_type    TEXT NOT NULL,
    target_path TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE (node_id, app_type, target_path)
);

-- 配置版本历史（draft/applied/rolled_back）
CREATE TABLE IF NOT EXISTS config_versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    target_id    INTEGER NOT NULL REFERENCES config_targets (id),
    content      TEXT NOT NULL,
    checksum     TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'draft',
    command_id   TEXT NOT NULL DEFAULT '',
    apply_result TEXT NOT NULL DEFAULT '{}',
    created_by   TEXT NOT NULL,
    created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cfgv_target ON config_versions (target_id, created_at);

-- 资源快照时序（按保留策略清理，默认 7 天）
CREATE TABLE IF NOT EXISTS metrics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id    TEXT NOT NULL,
    ts         INTEGER NOT NULL,
    cpu        REAL NOT NULL DEFAULT 0,
    mem_used   INTEGER NOT NULL DEFAULT 0,
    mem_total  INTEGER NOT NULL DEFAULT 0,
    disk_used  INTEGER NOT NULL DEFAULT 0,
    disk_total INTEGER NOT NULL DEFAULT 0,
    net_rx     REAL NOT NULL DEFAULT 0,
    net_tx     REAL NOT NULL DEFAULT 0,
    load1      REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_metrics_node ON metrics (node_id, ts);
-- 牧群吞吐时序按 ts 窗口过滤聚合(net-series),独立于 node 前导索引
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON metrics (ts);

-- 面板侧操作审计（不可删除）
CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL DEFAULT 0,
    username   TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '{}',
    ip         TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_logs (created_at);

-- MCP Keys(docs/07 §8):hash 存储,明文仅创建时返回一次
CREATE TABLE IF NOT EXISTS mcp_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    key_hash    TEXT NOT NULL UNIQUE,        -- sha256 hex
    scopes      TEXT NOT NULL DEFAULT '[]',  -- JSON: ["read","logs",...]
    node_scope  TEXT NOT NULL DEFAULT 'all', -- 'all' 或 JSON 数组 ["n_xxx"]
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_used   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
