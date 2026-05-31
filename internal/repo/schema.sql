-- cairn MVP metadata: the repo catalogue + the push audit log.
-- go-git owns object/ref storage on disk; this owns discovery + audit.
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS repo (
  id             TEXT PRIMARY KEY,             -- uuid
  org_id         TEXT NOT NULL,                -- herald org id (single-org MVP)
  slug           TEXT NOT NULL,                -- url-safe name within the org
  default_branch TEXT NOT NULL DEFAULT 'main',
  protection     TEXT NOT NULL DEFAULT '{}',   -- minimal default-branch rule (JSON)
  storage_path   TEXT NOT NULL,                -- absolute path to the bare repo on disk
  created_at     TEXT NOT NULL,                -- RFC3339
  updated_at     TEXT NOT NULL,                -- RFC3339
  UNIQUE(org_id, slug)
);

CREATE TABLE IF NOT EXISTS push_event (
  id              TEXT PRIMARY KEY,            -- uuid
  repo_id         TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
  ref             TEXT NOT NULL,               -- e.g. refs/heads/feature-x
  old_sha         TEXT NOT NULL,               -- zero-sha for create
  new_sha         TEXT NOT NULL,               -- zero-sha for delete
  pusher_agent_id TEXT NOT NULL,               -- herald agent id
  forced          INTEGER NOT NULL DEFAULT 0,  -- 1 if a non-fast-forward
  at              TEXT NOT NULL                -- RFC3339
);

CREATE INDEX IF NOT EXISTS idx_push_event_repo ON push_event(repo_id, at);
