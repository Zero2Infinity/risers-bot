CREATE TABLE IF NOT EXISTS sessions (
    id        TEXT PRIMARY KEY,          -- e.g. group JID or a UUID
    kind      TEXT NOT NULL DEFAULT 'cli',  -- 'group' | 'private' | 'cli'
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role       TEXT NOT NULL,            -- 'user' | 'assistant' | 'tool' | 'system'
    content    TEXT NOT NULL,            -- the prompt/reply/tool-arg/result
    thinking   TEXT,                     -- qwen reasoning (not fed back)
    tool_name  TEXT,                     -- if role='tool'
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
