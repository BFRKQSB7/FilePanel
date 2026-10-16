package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filepanel/migrations"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, "filepanel.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA secure_delete=ON"); err != nil {
		db.Close()
		return nil, err
	}
	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

// applyMigrations runs every embedded NNN_*.sql in order. Statements are
// idempotent (IF NOT EXISTS), so reapplying after a crash is safe.
func applyMigrations(db *sql.DB) error {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s has no numeric version", name)
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES (?)", version); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Setting(key string) (string, bool, error) {
	var value string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func (s *Store) DiskInfo(path string) (total, free, used uint64, err error) {
	return diskUsage(path)
}

func (s *Store) Now() int64 { return time.Now().Unix() }
