-- graph.db schema. Derived data only: there are no migrations. Bump
-- SchemaVersion (schema.go) on any change; mismatching databases are moved
-- aside and rebuilt from source.
--
-- Invariants:
--   * edges.file_id is the file whose facts produced the edge; edges.src is
--     always a node of that file.
--   * edges.dst NULL means unresolved; name_id keeps the target name so the
--     resolver can retry (see pending_names).
--   * names is append-only for the life of the database file.
--   * sym_fts / sym_tri rowid = nodes.id.

-- key/value state: schema_ver, extractor_ver:<lang>, state, progress,
-- last_scan_ns, gitindex_stat, bulk.
CREATE TABLE IF NOT EXISTS meta(
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;

-- status: see model.FileStatus (0 ok, 1 too_big, 2 generated, 3 minified,
-- 4 binary, 5 parse_err, 6 unsupported). blob = git blob SHA-1 (20 bytes).
CREATE TABLE IF NOT EXISTS files(
  id       INTEGER PRIMARY KEY,
  path     TEXT NOT NULL UNIQUE,
  lang     INTEGER NOT NULL DEFAULT 0,
  size     INTEGER NOT NULL DEFAULT 0,
  mtime_ns INTEGER NOT NULL DEFAULT 0,
  ino      INTEGER NOT NULL DEFAULT 0,
  mode     INTEGER NOT NULL DEFAULT 0,
  blob     BLOB,
  tracked  INTEGER NOT NULL DEFAULT 0,
  status   INTEGER NOT NULL DEFAULT 0,
  loc      INTEGER NOT NULL DEFAULT 0,
  pkg_id   INTEGER
);

-- untracked-cache: directory mtime, the stat signature of its .gitignore
-- (repo.StatSig string) and the encoded entry listing (repo.DirRow.Names),
-- so an unchanged directory is never re-read, even across processes.
CREATE TABLE IF NOT EXISTS dirs(
  path        TEXT PRIMARY KEY,
  mtime_ns    INTEGER NOT NULL DEFAULT 0,
  ignore_stat TEXT,
  names       BLOB NOT NULL DEFAULT x''
) WITHOUT ROWID;

-- interned identifiers.
CREATE TABLE IF NOT EXISTS names(
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

-- definitions. kind: model.NodeKind; flags: model.DefFlags; skey: 8 bytes.
CREATE TABLE IF NOT EXISTS nodes(
  id         INTEGER PRIMARY KEY,
  file_id    INTEGER NOT NULL,
  parent_id  INTEGER,
  kind       INTEGER NOT NULL,
  name_id    INTEGER NOT NULL,
  qname      TEXT NOT NULL,
  start_line INTEGER NOT NULL DEFAULT 0,
  start_col  INTEGER NOT NULL DEFAULT 0,
  end_line   INTEGER NOT NULL DEFAULT 0,
  start_byte INTEGER NOT NULL DEFAULT 0,
  end_byte   INTEGER NOT NULL DEFAULT 0,
  sig        TEXT,
  flags      INTEGER NOT NULL DEFAULT 0,
  skey       BLOB
);

-- kind: model.EdgeKind; conf 0..100.
CREATE TABLE IF NOT EXISTS edges(
  id      INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL,
  src     INTEGER NOT NULL,
  dst     INTEGER,
  kind    INTEGER NOT NULL,
  name_id INTEGER,
  qual    TEXT,
  line    INTEGER NOT NULL DEFAULT 0,
  col     INTEGER NOT NULL DEFAULT 0,
  conf    INTEGER NOT NULL DEFAULT 0
);

-- names: comma-separated imported member names.
CREATE TABLE IF NOT EXISTS imports(
  file_id     INTEGER NOT NULL,
  spec        TEXT NOT NULL,
  alias       TEXT,
  names       TEXT,
  target_file INTEGER,
  target_pkg  INTEGER,
  line        INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS packages(
  id   INTEGER PRIMARY KEY,
  lang INTEGER NOT NULL,
  name TEXT NOT NULL,
  dir  TEXT,
  kind INTEGER NOT NULL DEFAULT 0,
  UNIQUE(lang, name)
);

-- resolution queue; crash-safe because it commits with the facts.
CREATE TABLE IF NOT EXISTS pending_names(
  name_id INTEGER PRIMARY KEY
);

-- Files on whose behalf a repository answer (meta "dep:<key>", see
-- internal/index RepoCtx) was consulted, e.g. a module identity or import
-- target that depends on whether some file exists. When the answer
-- changes, only these files are reprocessed. file_id 0 marks an answer
-- consulted without an owner, which forces a full rebuild.
CREATE TABLE IF NOT EXISTS dep_owners(
  key     TEXT NOT NULL,
  file_id INTEGER NOT NULL,
  PRIMARY KEY(key, file_id)
) WITHOUT ROWID;

-- paths reported by PostToolUse hooks; ts in unix nanoseconds.
CREATE TABLE IF NOT EXISTS dirty_hints(
  path TEXT PRIMARY KEY,
  ts   INTEGER NOT NULL
) WITHOUT ROWID;

-- steering state per (session, agent).
CREATE TABLE IF NOT EXISTS sessions(
  sid      TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  agent    TEXT,
  started  INTEGER NOT NULL DEFAULT 0,
  last     INTEGER NOT NULL DEFAULT 0,
  denies   INTEGER NOT NULL DEFAULT 0,
  allows   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(sid, agent_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS session_patterns(
  sid      TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  pnorm    TEXT NOT NULL,
  tool     TEXT,
  denies   INTEGER NOT NULL DEFAULT 0,
  first    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(sid, agent_id, pnorm)
) WITHOUT ROWID;

-- hook decision dedupe; TTL enforced by readers (10 minutes).
CREATE TABLE IF NOT EXISTS hook_dedupe(
  k        TEXT PRIMARY KEY,
  decision TEXT NOT NULL,
  ts       INTEGER NOT NULL
) WITHOUT ROWID;

-- Full-text symbol search. tokens = TokenizeIdent(name); path = file path.
CREATE VIRTUAL TABLE IF NOT EXISTS sym_fts USING fts5(
  tokens, path,
  content='', contentless_delete=1,
  tokenize="unicode61 tokenchars '_$'",
  prefix='2 3'
);

-- Substring search over raw names.
CREATE VIRTUAL TABLE IF NOT EXISTS sym_tri USING fts5(
  name,
  content='', contentless_delete=1,
  tokenize='trigram'
);

-- Core indexes: always present, also during bulk builds, because per-file
-- replacement (DeleteFileGraph) depends on them.
CREATE INDEX IF NOT EXISTS nodes_file   ON nodes(file_id);
CREATE INDEX IF NOT EXISTS edges_file   ON edges(file_id);
CREATE INDEX IF NOT EXISTS edges_dst    ON edges(dst, kind) WHERE dst IS NOT NULL;
CREATE INDEX IF NOT EXISTS imports_file ON imports(file_id);
CREATE INDEX IF NOT EXISTS dep_owners_file ON dep_owners(file_id);
