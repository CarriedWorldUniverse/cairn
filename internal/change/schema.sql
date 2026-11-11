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
  at          TEXT NOT NULL,
  -- seq is the log's ORDER: an explicit, monotonic sequence assigned at insert
  -- (#173). Ordering by rowid worked (#157) but rests on a property SQLite only
  -- practically guarantees for a TEXT PRIMARY KEY table — VACUUM may renumber
  -- rowids. seq is backfilled from rowid for existing repos by migrate.
  seq         INTEGER NOT NULL DEFAULT 0
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
-- Generation number per commit: 1 for a root, else 1 + the highest parent's.
-- A commit's generation never changes (commits are content-addressed), so
-- this is a pure cache; graph walks order by it to get exact answers
-- regardless of commit timestamps (graph.go).
CREATE TABLE IF NOT EXISTS commit_gen (
  sha TEXT PRIMARY KEY,
  gen INTEGER NOT NULL
) WITHOUT ROWID;
-- A rebase onto the parent line that stopped at a conflicting commit (pull):
-- the commits still to replay, and the operator's un-sealed work, as JSON.
-- `cairn commit` seals the stopped commit and replays the rest.
CREATE TABLE IF NOT EXISTS rebase_state (
  line_id TEXT PRIMARY KEY REFERENCES line(id) ON DELETE CASCADE,
  state   TEXT NOT NULL
);
-- What rebase_state was for a line BEFORE an operation changed it, so undo of
-- that operation puts it back ('' = no rebase in progress).
CREATE TABLE IF NOT EXISTS rebase_history (
  op_id   TEXT NOT NULL,
  line_id TEXT NOT NULL,
  before  TEXT NOT NULL,
  PRIMARY KEY (op_id, line_id)
);
-- Inverse steps for operations whose effect is not captured by line tips
-- (tag create/delete, line rename, remote edits): Undo applies them, newest
-- step first, after it restores the tips.
CREATE TABLE IF NOT EXISTS op_undo (
  op_id TEXT PRIMARY KEY,
  steps TEXT NOT NULL
);
-- Reversible side state. The stash stack and the open-conflict rows are not
-- part of the ref-map view an operation records, so these triggers journal
-- every row they lose or change (old row as JSON, '' op_id = not yet claimed).
-- recordOp/recordOpTx claim the unclaimed rows for the operation they append,
-- and Undo replays an operation's rows backwards (auxjournal.go).
CREATE TABLE IF NOT EXISTS aux_journal (
  seq    INTEGER PRIMARY KEY AUTOINCREMENT,
  op_id  TEXT NOT NULL DEFAULT '',
  tbl    TEXT NOT NULL,
  kind   TEXT NOT NULL,
  row_id TEXT NOT NULL,
  old    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_aux_journal_op ON aux_journal(op_id);
CREATE TRIGGER IF NOT EXISTS stash_aux_ins AFTER INSERT ON stash BEGIN
  INSERT INTO aux_journal(tbl, kind, row_id) VALUES('stash', 'I', NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS stash_aux_del AFTER DELETE ON stash BEGIN
  INSERT INTO aux_journal(tbl, kind, row_id, old) VALUES('stash', 'D', OLD.id, json_object(
    'id', OLD.id, 'line_id', OLD.line_id, 'branch', OLD.branch, 'commit_sha', OLD.commit_sha,
    'base_sha', OLD.base_sha, 'message', OLD.message, 'created_at', OLD.created_at));
END;
CREATE TRIGGER IF NOT EXISTS conflict_aux_ins AFTER INSERT ON conflict BEGIN
  INSERT INTO aux_journal(tbl, kind, row_id) VALUES('conflict', 'I', NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS conflict_aux_del AFTER DELETE ON conflict BEGIN
  INSERT INTO aux_journal(tbl, kind, row_id, old) VALUES('conflict', 'D', OLD.id, json_object(
    'id', OLD.id, 'change_id', OLD.change_id, 'path', OLD.path, 'base_blob', OLD.base_blob,
    'parent_blob', OLD.parent_blob, 'change_blob', OLD.change_blob, 'marked_blob', OLD.marked_blob,
    'status', OLD.status, 'created_at', OLD.created_at, 'resolved_at', OLD.resolved_at));
END;
CREATE TRIGGER IF NOT EXISTS conflict_aux_upd AFTER UPDATE ON conflict BEGIN
  INSERT INTO aux_journal(tbl, kind, row_id, old) VALUES('conflict', 'U', OLD.id, json_object(
    'id', OLD.id, 'change_id', OLD.change_id, 'path', OLD.path, 'base_blob', OLD.base_blob,
    'parent_blob', OLD.parent_blob, 'change_blob', OLD.change_blob, 'marked_blob', OLD.marked_blob,
    'status', OLD.status, 'created_at', OLD.created_at, 'resolved_at', OLD.resolved_at));
END;
