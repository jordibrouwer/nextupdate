CREATE TABLE IF NOT EXISTS history (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  container   TEXT    NOT NULL,
  image       TEXT    NOT NULL,
  from_image  TEXT    NOT NULL,
  to_image    TEXT    NOT NULL,
  started_at  INTEGER NOT NULL,
  finished_at INTEGER NOT NULL,
  outcome     TEXT    NOT NULL,
  reason      TEXT    NOT NULL DEFAULT '',
  log         TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS journal (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  container TEXT    NOT NULL,
  adapter   TEXT    NOT NULL,
  step      TEXT    NOT NULL,
  data      TEXT    NOT NULL DEFAULT '{}',
  closed    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS available (
  container     TEXT PRIMARY KEY,
  image         TEXT    NOT NULL,
  local_digest  TEXT    NOT NULL,
  remote_digest TEXT    NOT NULL,
  detected_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS container_settings (
  container            TEXT PRIMARY KEY,
  policy               TEXT    NOT NULL DEFAULT 'notify',
  http_url             TEXT    NOT NULL DEFAULT '',
  repo                 TEXT    NOT NULL DEFAULT '',
  verify_window_seconds INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS available_info (
  container   TEXT PRIMARY KEY,
  old_version TEXT    NOT NULL DEFAULT '',
  new_version TEXT    NOT NULL DEFAULT '',
  repo        TEXT    NOT NULL DEFAULT '',
  breaking    INTEGER NOT NULL DEFAULT 0,
  reasons     TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS changelog_cache (
  repo       TEXT PRIMARY KEY,
  etag       TEXT    NOT NULL,
  body       BLOB    NOT NULL,
  fetched_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS old_images (
  image_id     TEXT PRIMARY KEY,
  container    TEXT    NOT NULL,
  remove_after INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS seen (
  container TEXT NOT NULL,
  kind      TEXT NOT NULL,
  digest    TEXT NOT NULL,
  PRIMARY KEY (container, kind)
);

CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT    NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT    NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS notifiers (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  name    TEXT    NOT NULL,
  type    TEXT    NOT NULL,
  config  TEXT    NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS push_subscriptions (
  endpoint TEXT PRIMARY KEY,
  p256dh   TEXT    NOT NULL,
  auth     TEXT    NOT NULL,
  user_id  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0
);
