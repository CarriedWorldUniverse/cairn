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

CREATE TABLE IF NOT EXISTS pull_request (
  id               TEXT PRIMARY KEY,            -- 16-byte hex
  repo_id          TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
  source_ref       TEXT NOT NULL,               -- branch name, e.g. "feature"
  target_ref       TEXT NOT NULL,               -- branch name, e.g. "main"
  title            TEXT NOT NULL,
  ledger_issue_key TEXT NOT NULL,               -- e.g. "ACME-7"
  state            TEXT NOT NULL DEFAULT 'open', -- 'open' | 'merged' | 'closed'
  opened_by        TEXT NOT NULL,               -- X-CWB-Subject of the opener
  created_at       TEXT NOT NULL                -- RFC3339
);

-- At most one OPEN pr per (repo, source, target).
CREATE UNIQUE INDEX IF NOT EXISTS pr_open_uniq
  ON pull_request(repo_id, source_ref, target_ref) WHERE state = 'open';

-- embargo_recipient: identities authorized to fetch a repo's EMBARGOED content
-- (the real bytes from the per-repo embargo bare). cairn owns this ACL — herald
-- scopes are too coarse (org-level repo:read/write). All-or-nothing per repo.
CREATE TABLE IF NOT EXISTS embargo_recipient (
  repo_id    TEXT NOT NULL REFERENCES repo(id) ON DELETE CASCADE,
  agent_id   TEXT NOT NULL,  -- herald agent id (SSH agent.ID; HTTP X-CWB-Subject)
  granted_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (repo_id, agent_id)
);
