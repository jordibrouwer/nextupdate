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
