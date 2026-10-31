package store

import (
	"context"
	"fmt"
	"strings"
)

var Tables = []string{
	"kv", "users", "identities", "sessions", "api_tokens", "recovery_codes", "mod_log", "projects", "versions",
	"issues", "issue_labels", "issue_versions", "field_values", "comments",
	"attachments", "history", "votes", "watchers", "links", "confirmations",
	"notifications", "saved_searches", "commits", "automation_runs",
}

var migrations = []string{
	`
CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);

CREATE TABLE users (
  id {{pk}},
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'user',
  avatar_url TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  locale TEXT NOT NULL DEFAULT '',
  email_notifications BOOLEAN NOT NULL DEFAULT TRUE,
  banned BOOLEAN NOT NULL DEFAULT FALSE,
  shadow_banned BOOLEAN NOT NULL DEFAULT FALSE,
  trusted BOOLEAN NOT NULL DEFAULT FALSE,
  system BOOLEAN NOT NULL DEFAULT FALSE,
  created_at BIGINT NOT NULL,
  last_seen_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX users_email ON users (email);

CREATE TABLE identities (
  provider TEXT NOT NULL,
  subject TEXT NOT NULL,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (provider, subject)
);

CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE api_tokens (
  id {{pk}},
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  last_used_at BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE projects (
  id {{pk}},
  key TEXT NOT NULL UNIQUE,
  next_number BIGINT NOT NULL DEFAULT 1,
  created_at BIGINT NOT NULL
);

CREATE TABLE versions (
  id {{pk}},
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  sort_order BIGINT NOT NULL DEFAULT 0,
  released BOOLEAN NOT NULL DEFAULT FALSE,
  release_date TEXT NOT NULL DEFAULT '',
  archived BOOLEAN NOT NULL DEFAULT FALSE,
  UNIQUE (project_id, name)
);

CREATE TABLE issues (
  id {{pk}},
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number BIGINT NOT NULL,
  key TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  resolution TEXT NOT NULL DEFAULT '',
  reporter_id BIGINT NOT NULL REFERENCES users(id),
  assignee_id BIGINT NOT NULL DEFAULT 0,
  private BOOLEAN NOT NULL DEFAULT FALSE,
  moderation TEXT NOT NULL DEFAULT 'approved',
  votes BIGINT NOT NULL DEFAULT 0,
  watchers BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  confirmations BIGINT NOT NULL DEFAULT 0,
  fingerprint TEXT NOT NULL DEFAULT '',
  duplicate_of BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  resolved_at BIGINT NOT NULL DEFAULT 0,
  activity_at BIGINT NOT NULL,
  UNIQUE (project_id, number)
);
CREATE INDEX issues_state ON issues (project_id, state);
CREATE INDEX issues_reporter ON issues (reporter_id);
CREATE INDEX issues_assignee ON issues (assignee_id);
CREATE INDEX issues_fingerprint ON issues (fingerprint);
CREATE INDEX issues_updated ON issues (updated_at);
CREATE INDEX issues_votes ON issues (votes);

CREATE TABLE issue_labels (
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  PRIMARY KEY (issue_id, label)
);
CREATE INDEX issue_labels_label ON issue_labels (label);

CREATE TABLE issue_versions (
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  version_id BIGINT NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  PRIMARY KEY (issue_id, version_id, kind)
);
CREATE INDEX issue_versions_version ON issue_versions (version_id, kind);

CREATE TABLE field_values (
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  field TEXT NOT NULL,
  value TEXT NOT NULL,
  num DOUBLE PRECISION
);
CREATE INDEX field_values_issue ON field_values (issue_id);
CREATE INDEX field_values_lookup ON field_values (field, value);

CREATE TABLE comments (
  id {{pk}},
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  author_id BIGINT NOT NULL REFERENCES users(id),
  body TEXT NOT NULL,
  restricted BOOLEAN NOT NULL DEFAULT FALSE,
  moderation TEXT NOT NULL DEFAULT 'approved',
  deleted BOOLEAN NOT NULL DEFAULT FALSE,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE INDEX comments_issue ON comments (issue_id, created_at);
CREATE INDEX comments_author ON comments (author_id);

CREATE TABLE attachments (
  id {{pk}},
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  uploader_id BIGINT NOT NULL REFERENCES users(id),
  filename TEXT NOT NULL,
  content_type TEXT NOT NULL,
  size BIGINT NOT NULL,
  storage_key TEXT NOT NULL,
  deleted BOOLEAN NOT NULL DEFAULT FALSE,
  created_at BIGINT NOT NULL
);
CREATE INDEX attachments_issue ON attachments (issue_id);

CREATE TABLE history (
  id {{pk}},
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  actor_id BIGINT NOT NULL,
  field TEXT NOT NULL,
  old_value TEXT NOT NULL DEFAULT '',
  new_value TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE INDEX history_issue ON history (issue_id, created_at);
CREATE INDEX history_actor ON history (actor_id);

CREATE TABLE votes (
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (issue_id, user_id)
);
CREATE INDEX votes_user ON votes (user_id);
CREATE INDEX votes_created ON votes (created_at);

CREATE TABLE watchers (
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (issue_id, user_id)
);
CREATE INDEX watchers_user ON watchers (user_id);

CREATE TABLE links (
  id {{pk}},
  source_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  target_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  created_by BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  UNIQUE (source_id, target_id, kind)
);
CREATE INDEX links_target ON links (target_id);

CREATE TABLE confirmations (
  id {{pk}},
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  version_id BIGINT NOT NULL DEFAULT 0,
  env TEXT NOT NULL DEFAULT '{}',
  note TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE INDEX confirmations_issue ON confirmations (issue_id);

CREATE TABLE notifications (
  id {{pk}},
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  issue_id BIGINT NOT NULL DEFAULT 0,
  actor_id BIGINT NOT NULL DEFAULT 0,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  read BOOLEAN NOT NULL DEFAULT FALSE,
  created_at BIGINT NOT NULL
);
CREATE INDEX notifications_user ON notifications (user_id, read, created_at);

CREATE TABLE saved_searches (
  id {{pk}},
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  query TEXT NOT NULL,
  public BOOLEAN NOT NULL DEFAULT FALSE,
  created_at BIGINT NOT NULL
);

CREATE TABLE commits (
  id {{pk}},
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  sha TEXT NOT NULL,
  message TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  UNIQUE (issue_id, sha)
);

CREATE TABLE automation_runs (
  automation TEXT NOT NULL,
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  ran_at BIGINT NOT NULL,
  issue_updated_at BIGINT NOT NULL,
  PRIMARY KEY (automation, issue_id)
);
`,
	`{{fts}}`,
	`
CREATE TABLE recovery_codes (
  id {{pk}},
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL
);
CREATE INDEX recovery_codes_user ON recovery_codes (user_id, code_hash);
`,
	`
ALTER TABLE users ADD COLUMN ban_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN banned_until BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN muted_until BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN mute_reason TEXT NOT NULL DEFAULT '';
CREATE TABLE mod_log (
  id {{pk}},
  actor_id BIGINT NOT NULL,
  target_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  until_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL
);
CREATE INDEX mod_log_target ON mod_log (target_id, created_at);
`,
}

const ftsSQLite = `CREATE VIRTUAL TABLE issue_fts USING fts5(title, body, tokenize = 'porter unicode61 remove_diacritics 2');`

const ftsPostgres = `
CREATE TABLE issue_fts (
  issue_id BIGINT PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  tsv tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(body, '')), 'B')
  ) STORED
);
CREATE INDEX issue_fts_tsv ON issue_fts USING GIN (tsv);
CREATE INDEX issues_title_trgm ON issues (lower(title));
`

func (d *DB) dialectSQL(s string) string {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	fts := ftsSQLite
	if d.driver == "postgres" {
		pk = "BIGSERIAL PRIMARY KEY"
		fts = ftsPostgres
	}
	s = strings.ReplaceAll(s, "{{pk}}", pk)
	s = strings.ReplaceAll(s, "{{fts}}", fts)
	return s
}

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, applied_at BIGINT NOT NULL)`); err != nil {
		return err
	}
	var cur int
	if err := d.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&cur); err != nil {
		return err
	}
	for i := cur; i < len(migrations); i++ {
		v := i + 1
		err := d.InTx(ctx, func(tx *Tx) error {
			for _, stmt := range splitSQL(d.dialectSQL(migrations[i])) {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					return fmt.Errorf("migration %d: %w\n%s", v, err, stmt)
				}
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, now())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func splitSQL(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (d *DB) SchemaVersion(ctx context.Context) int {
	var v int
	d.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v
}
