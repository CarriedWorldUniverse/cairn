PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS line (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  parent_line TEXT REFERENCES line(id),
  tip_commit  TEXT NOT NULL DEFAULT '',
  base_commit TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS change (
  id           TEXT PRIMARY KEY,
  line_id      TEXT NOT NULL REFERENCES line(id) ON DELETE CASCADE,
  author       TEXT NOT NULL,
  head_commit  TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'open',
  has_conflict INTEGER NOT NULL DEFAULT 0,
  sealed       INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS conflict (
  id          TEXT PRIMARY KEY,
  change_id   TEXT NOT NULL REFERENCES change(id) ON DELETE CASCADE,
  path        TEXT NOT NULL,
  base_blob   TEXT NOT NULL DEFAULT '',
  parent_blob TEXT NOT NULL DEFAULT '',
  change_blob TEXT NOT NULL DEFAULT '',
  marked_blob TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  resolved_at TEXT
);
CREATE TABLE IF NOT EXISTS tag (
  name   TEXT PRIMARY KEY,
  commit_sha TEXT NOT NULL,
  tagger TEXT NOT NULL,
  at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS operation (
  id          TEXT PRIMARY KEY,
  op_type     TEXT NOT NULL,
  actor       TEXT NOT NULL,
  parent_op   TEXT NOT NULL DEFAULT '',
  view_before TEXT NOT NULL,
  view_after  TEXT NOT NULL,
  detail      TEXT NOT NULL DEFAULT '{}',
  at          TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS remote_kind (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL DEFAULT 'git'   -- 'git' | 'cairn'
);
CREATE TABLE IF NOT EXISTS config (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS stash (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  line_id TEXT NOT NULL,
  branch TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  base_sha TEXT NOT NULL,
  message TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bisect (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  line_id     TEXT NOT NULL,
  branch      TEXT NOT NULL,
  good_sha    TEXT NOT NULL,
  bad_sha     TEXT NOT NULL,
  current_sha TEXT NOT NULL,
  restore_tip TEXT NOT NULL,
  started_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS privacy (
  path       TEXT PRIMARY KEY,
  mode       TEXT NOT NULL CHECK (mode IN ('shape-only','omit')),
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS embargo (
  commit_sha TEXT PRIMARY KEY,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_change_line ON change(line_id);
CREATE INDEX IF NOT EXISTS idx_conflict_change ON conflict(change_id);
