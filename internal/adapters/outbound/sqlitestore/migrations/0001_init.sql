-- Sessões, mensagens, memória longa e auditoria de ferramentas.
-- Tempos em milissegundos desde a época Unix.

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX sessions_updated ON sessions (updated_at);

CREATE TABLE messages (
    id         INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    origin     TEXT NOT NULL,
    content    TEXT NOT NULL,
    tool_name  TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX messages_session ON messages (session_id, id);

-- embedding e embed_model ficam vazios até a Fase 2.
CREATE TABLE memories (
    id          INTEGER PRIMARY KEY,
    text        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    source      TEXT NOT NULL,
    trust       TEXT NOT NULL CHECK (trust IN ('high', 'low')),
    embedding   BLOB,
    embed_model TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE VIRTUAL TABLE memories_fts USING fts5 (
    text,
    content = 'memories',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts (rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER memories_au AFTER UPDATE OF text ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO memories_fts (rowid, text) VALUES (new.id, new.text);
END;

CREATE TABLE tool_runs (
    id          INTEGER PRIMARY KEY,
    session_id  TEXT NOT NULL,
    trace_id    TEXT NOT NULL,
    tool        TEXT NOT NULL,
    args        TEXT NOT NULL,
    risk        TEXT NOT NULL,
    decision    TEXT NOT NULL,
    result      TEXT NOT NULL,
    error       TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX tool_runs_session ON tool_runs (session_id, id);
