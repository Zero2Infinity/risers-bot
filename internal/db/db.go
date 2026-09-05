package db

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

// Message is one row of a per-session chat log.
type Message struct {
	ID        int64
	SessionID string
	Role      string // 'user' | 'assistant' | 'tool' | 'system'
	Content   string
	Thinking  string // qwen reasoning (not fed back to the model)
	ToolName  string // set when Role == 'tool'
	CreatedAt string
}

// Store wraps a SQLite database.
type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) GetOrCreateSession(id, kind string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("session id empty")
	}

	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "cli"
	}

	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions(id, kind) VALUES (?, ?)`,
		id, kind,
	)

	if err != nil {
		return false, fmt.Errorf("insert session %q: %w", id, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}

	// n == 1 -> newly created, n == 0 -> already existed
	return n > 0, nil
}

func (s *Store) SaveMessage(sessionID, role, content, thinking, toolName string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id empty")
	}

	_, err := s.db.Exec(
		`INSERT INTO messages(session_id, role, content, thinking, tool_name)
		VALUES(?, ?, ?, ?, ?)`,
		sessionID, role, content, thinking, toolName,
	)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	return nil
}

func (s *Store) ListMessages(sessionID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, role, content, thinking, tool_name, created_at
		FROM messages WHERE session_id = ? ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.SessionID, &m.Role, &m.Content,
			&m.Thinking, &m.ToolName, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan messages: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	return msgs, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
