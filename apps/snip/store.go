package main

import (
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS links (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT UNIQUE NOT NULL,
	url TEXT NOT NULL,
	clicks INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

var ErrNotFound = errors.New("link not found")

type Link struct {
	URL    string
	Clicks int64
}

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateLink(url string) (code string, err error) {
	result, err := s.db.Exec("INSERT INTO links (code, url) VALUES ('', ?)", url)
	if err != nil {
		return "", fmt.Errorf("failed to insert link: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("failed to read inserted id: %w", err)
	}
	code = Encode(id)
	if _, err := s.db.Exec("UPDATE links SET code = ? WHERE id = ?", code, id); err != nil {
		return "", fmt.Errorf("failed to set code: %w", err)
	}
	return code, nil
}

func (s *Store) GetLink(code string) (Link, error) {
	var link Link
	err := s.db.QueryRow("SELECT url, clicks FROM links WHERE code = ?", code).Scan(&link.URL, &link.Clicks)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("failed to get link: %w", err)
	}
	return link, nil
}

func (s *Store) IncrementClicks(code string) error {
	if _, err := s.db.Exec("UPDATE links SET clicks = clicks + 1 WHERE code = ?", code); err != nil {
		return fmt.Errorf("failed to increment clicks: %w", err)
	}
	return nil
}