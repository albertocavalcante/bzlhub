-- canopy storage + index schema (SQLite).
--
-- Two layers:
--   * Relational tables hold the canonical structured ModuleReport content.
--   * FTS5 virtual tables index searchable text with the trigram tokenizer
--     for fuzzy/substring matching (sub-10ms on this corpus shape).
--
-- All writes go through internal/store/store.go; this file is loaded once
-- at Open() time via Migrate().

PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

-- ----------------------------------------------------------------------------
-- Canonical structured tables.
-- ----------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS modules (
    name TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS versions (
    module_name         TEXT NOT NULL,
    version             TEXT NOT NULL,
    compatibility_level INTEGER NOT NULL DEFAULT 0,
    bazel_compatibility TEXT NOT NULL DEFAULT '',  -- JSON-encoded []string
    ingested_at         TEXT NOT NULL DEFAULT (datetime('now')),
    -- Hermeticity is stored as JSON-encoded HermeticityProfile.
    hermeticity_json    TEXT NOT NULL DEFAULT '{}',
    -- Compressed tarball size in bytes (from the upstream blob).
    -- 0 means "unknown" (pre-migration row or non-tarball ingest).
    tarball_size        INTEGER NOT NULL DEFAULT 0,
    -- Cached bool (0/1) for "does this (module, version)'s SCIP blob
    -- contain at least one indexed Starlark document?" Read by the
    -- modules listing + search hit projection; written by the ingest
    -- path on each WriteScipBlob. A backfill at boot reconciles rows
    -- whose blobs predate this column (additive migration in store.go).
    has_source_index    INTEGER NOT NULL DEFAULT 0,
    -- The full report is stored verbatim for re-hydration into report.ModuleReport.
    report_json         TEXT NOT NULL,
    PRIMARY KEY (module_name, version),
    FOREIGN KEY (module_name) REFERENCES modules(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_versions_module ON versions(module_name);

-- A rule symbol can legitimately exist multiple times in a module under
-- different files (e.g., rules_cc defines `cc_toolchain_config` once per
-- platform in separate .bzl files). The key includes `file` to allow this.
CREATE TABLE IF NOT EXISTS rules (
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    name        TEXT NOT NULL,
    file        TEXT NOT NULL DEFAULT '',
    line        INTEGER NOT NULL DEFAULT 0,
    doc         TEXT NOT NULL DEFAULT '',
    private     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (module_name, version, name, file),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_rules_name ON rules(name);

CREATE TABLE IF NOT EXISTS providers (
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    name        TEXT NOT NULL,
    file        TEXT NOT NULL DEFAULT '',
    line        INTEGER NOT NULL DEFAULT 0,
    doc         TEXT NOT NULL DEFAULT '',
    private     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (module_name, version, name, file),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_providers_name ON providers(name);

-- Hermeticity classes (one row per (module, version, class)) for SQL filtering.
-- The full HermeticityProfile (with findings + provenance) is in versions.hermeticity_json.
CREATE TABLE IF NOT EXISTS hermeticity_classes (
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    class       TEXT NOT NULL,
    PRIMARY KEY (module_name, version, class),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

-- ----------------------------------------------------------------------------
-- FTS5 indexes — trigram tokenizer for fuzzy/substring matching.
--
-- We index each searchable surface as a separate row in a contentless FTS
-- table; the row's `match_kind` distinguishes which kind of thing matched,
-- and the rowid points back to the relational row.
-- ----------------------------------------------------------------------------

CREATE VIRTUAL TABLE IF NOT EXISTS fts_text USING fts5(
    text,                              -- the searchable token stream
    tokenize = 'trigram'
);

-- Side table linking FTS rowids to (module, version, kind, name, file).
-- `file` disambiguates same-named symbols defined in multiple .bzl files
-- (rules_cc-style); empty string for "module" rows and for entries
-- without provenance.
CREATE TABLE IF NOT EXISTS fts_meta (
    rowid       INTEGER PRIMARY KEY,
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    kind        TEXT NOT NULL,    -- "module" | "rule" | "provider" | "macro"
    name        TEXT NOT NULL,
    file        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_fts_meta_module ON fts_meta(module_name, version);

-- audit_events: durable record of write operations against canopy. Read
-- ops (search, list) are NOT logged here — they're high-volume and the
-- absence of an audit trail isn't a question for "what happened?"
--
-- The kind taxonomy is open (TEXT) so we can extend later without a
-- migration. Today: bump_success | bump_failure | ingest_recursive_success
-- | ingest_recursive_failure | ingest_dir_success | ingest_dir_failure.
--
-- payload is JSON-marshaled for structured detail (deps counts,
-- rule counts, integrity strings, etc.) without bloating the column set.
-- Per-module SCIP indexes. Persisted as binary protobuf bytes
-- produced by canopy/internal/scip (which wraps scip-bazel). One row
-- per (module, version); REPLACE on re-ingest. Kept in a separate
-- table from versions(report_json) so the index can be (re)generated
-- independently of the structured ModuleReport.
CREATE TABLE IF NOT EXISTS module_scip (
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    indexer     TEXT NOT NULL DEFAULT 'scip-bazel',
    indexed_at  TEXT NOT NULL DEFAULT (datetime('now')),
    blob        BLOB NOT NULL,
    PRIMARY KEY (module_name, version),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS audit_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT    NOT NULL,             -- RFC3339Nano UTC
    kind        TEXT    NOT NULL,
    source      TEXT    NOT NULL DEFAULT 'unknown',
    module      TEXT,                          -- nullable for non-module events
    version     TEXT,                          -- nullable
    ok          INTEGER NOT NULL,              -- 1 if success, 0 if failure
    duration_ms INTEGER,
    error       TEXT,
    payload     TEXT,                          -- JSON; nullable
    -- Authenticated identity (DisplayName) at request time when
    -- the request was authenticated via the header-auth scaffold
    -- or a bearer token. NULL for anonymous + pre-migration rows.
    user_id     TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_ts        ON audit_events(ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_kind_ts   ON audit_events(kind, ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_module_ts ON audit_events(module, ts DESC);

-- module_github_meta: per-module social signals + languages, refreshed
-- from the GitHub REST API on a 6h interval (and on every Bump). One
-- row per indexed module; refresh sweeps update in place.
--
-- meta_json carries the full canopy/internal/githubmeta.Meta struct
-- (etag, language byte counts, description, etc.) so adding GitHub
-- fields doesn't require a migration. The flat columns exist for
-- SQL-side ordering / filtering ("modules sorted by stars").
--
-- http_status records the last terminal state the refresher saw:
--   200  → fresh row, meta_json valid
--   304  → not modified; fetched_at updated, meta_json unchanged
--   404  → repo no longer exists on GitHub
--   429  → rate-limited; row preserved, refresh deferred
CREATE TABLE IF NOT EXISTS module_github_meta (
    module_name TEXT PRIMARY KEY,
    owner       TEXT NOT NULL,
    repo        TEXT NOT NULL,
    stars       INTEGER NOT NULL DEFAULT 0,
    forks       INTEGER NOT NULL DEFAULT 0,
    watchers    INTEGER NOT NULL DEFAULT 0,
    primary_language TEXT NOT NULL DEFAULT '',
    etag        TEXT NOT NULL DEFAULT '',
    http_status INTEGER NOT NULL DEFAULT 0,
    fetched_at  TEXT NOT NULL,
    meta_json   TEXT NOT NULL DEFAULT '{}',
    FOREIGN KEY (module_name) REFERENCES modules(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_github_meta_fetched ON module_github_meta(fetched_at);
CREATE INDEX IF NOT EXISTS idx_github_meta_stars   ON module_github_meta(stars DESC);

-- External URL surface — one row per (URL, platform, file, rule_name) tuple
-- per module-version. Populated by canopy/internal/external during ingest
-- via assay/interp/external.Analyze. The schema is denormalized for fast
-- "give me every URL this module fetches" reads.
CREATE TABLE IF NOT EXISTS external_refs (
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    url         TEXT NOT NULL,
    host        TEXT NOT NULL DEFAULT '',
    class       TEXT NOT NULL DEFAULT '',
    mutability  TEXT NOT NULL DEFAULT '',
    sha256      TEXT NOT NULL DEFAULT '',
    integrity   TEXT NOT NULL DEFAULT '',
    api_name    TEXT NOT NULL DEFAULT '',
    rule_name   TEXT NOT NULL DEFAULT '',
    platform    TEXT NOT NULL DEFAULT 'any',
    tainted     INTEGER NOT NULL DEFAULT 0,
    file        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (module_name, version, url, platform, file, rule_name),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_external_refs_module ON external_refs(module_name, version);
CREATE INDEX IF NOT EXISTS idx_external_refs_class  ON external_refs(class);
CREATE INDEX IF NOT EXISTS idx_external_refs_host   ON external_refs(host);

-- Per-fork interpretation errors from external analysis. Diagnostic only;
-- the corresponding external_refs rows still ship for forks that succeeded.
CREATE TABLE IF NOT EXISTS external_fork_errors (
    module_name   TEXT NOT NULL,
    version       TEXT NOT NULL,
    platform      TEXT NOT NULL DEFAULT 'any',
    error_message TEXT NOT NULL,
    PRIMARY KEY (module_name, version, platform, error_message),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_external_fork_errors_module ON external_fork_errors(module_name, version);

-- Cross-module index of use_extension() call sites + their tag invocations.
-- Populated by canopy ingest from every module's MODULE.bazel; queried by
-- the airgap analyzer when re-driving a producer ruleset's
-- module_extension impls — the corpus of real tag values is what turns
-- "default-attr extension drive" (synthetic, often empty URLs) into
-- "real consumer-driven extension drive" (concrete URLs).
--
-- One row per (consumer_module, consumer_version, extension_file,
-- extension_name, tag_index). Sites with zero tags don't get rows
-- (uninteresting for the tag-aggregation use case).
CREATE TABLE IF NOT EXISTS module_extension_usages (
    consumer_module   TEXT NOT NULL,
    consumer_version  TEXT NOT NULL,
    extension_file    TEXT NOT NULL,
    extension_name    TEXT NOT NULL,
    tag_index         INTEGER NOT NULL,
    tag_name          TEXT NOT NULL,
    tag_attrs_json    TEXT NOT NULL DEFAULT '{}',
    dev_dependency    INTEGER NOT NULL DEFAULT 0,
    isolate           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (consumer_module, consumer_version, extension_file, extension_name, tag_index),
    -- FK column names differ from the referenced columns by design: the
    -- local pair (consumer_module, consumer_version) reads more clearly
    -- in a usages table than (module_name, version), which here would be
    -- ambiguous with the producer module. SQLite matches FKs by position,
    -- so the (consumer_module, consumer_version) → (module_name, version)
    -- mapping is correct despite the rename.
    FOREIGN KEY (consumer_module, consumer_version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_module_extension_usages_ext
    ON module_extension_usages(extension_file, extension_name);

-- Per-module persistent storage of the .bzl source bytes for files
-- that declare module_extension(). Used at query time to re-drive
-- producer extension impls with consumer-corpus-derived ModuleSpecs —
-- the bridge between "we know what tag values consumers pin" and
-- "we know what URLs that produces."
--
-- One row per (module, version, file). Only files that declare at
-- least one module_extension are stored (typically 1-5 per producer
-- ruleset). Full tarball sources are NOT preserved — only what's
-- needed to re-eval the extension impl.
CREATE TABLE IF NOT EXISTS module_extension_sources (
    module_name  TEXT NOT NULL,
    version      TEXT NOT NULL,
    file         TEXT NOT NULL,
    content      BLOB NOT NULL,
    PRIMARY KEY (module_name, version, file),
    FOREIGN KEY (module_name, version) REFERENCES versions(module_name, version) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_module_extension_sources_module
    ON module_extension_sources(module_name, version);

-- module_sources: provenance + collision audit for Plan 16
-- federation. One row per (module, version, source_url) tuple:
--   'local'              — served from the local --root mirror
--   'http-upstream'      — served from an --upstream cascade probe
--   'collision-shadowed' — present in this upstream but a
--                          higher-priority source already served it
--
-- PK includes source_url so the same (m, v) can legitimately appear
-- from multiple sources without overwriting — that's the whole
-- point. Plan 16's draft used (m, v, source_kind) which silently
-- clobbered when the same module appeared in two HTTP upstreams.
--
-- No FK to versions(module_name, version) — federation can serve
-- (m, v) that isn't locally indexed yet (that's its whole purpose).
-- The audit row exists even if the module never gets locally Bumped.
CREATE TABLE IF NOT EXISTS module_sources (
    module_name TEXT NOT NULL,
    version     TEXT NOT NULL,
    source_url  TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK(source_kind IN (
        'local',
        'http-upstream',
        'collision-shadowed'
    )),
    seen_at     TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (module_name, version, source_url)
);

CREATE INDEX IF NOT EXISTS idx_module_sources_module
    ON module_sources(module_name);
CREATE INDEX IF NOT EXISTS idx_module_sources_collisions
    ON module_sources(source_kind)
    WHERE source_kind = 'collision-shadowed';
CREATE INDEX IF NOT EXISTS idx_module_sources_seen
    ON module_sources(seen_at DESC);
