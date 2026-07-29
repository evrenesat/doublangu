// Package store provides the SQLite/WAL database connection, migration runner,
// and transactional test helpers for the single-owner Doublangu server.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB connection with WAL mode and applied migrations.
type DB struct {
	conn *sql.DB
}

// Open creates or opens the SQLite database at path, sets WAL journal mode,
// runs any pending migrations, and returns the ready handle.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	conn.SetMaxOpenConns(1) // SQLite serializes writes; one connection is correct for WAL.
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)
	if _, err := conn.Exec("PRAGMA journal_mode = WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: enable WAL %q: %w", path, err)
	}
	if _, err := conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: enable foreign keys %q: %w", path, err)
	}
	if _, err := conn.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: set busy timeout %q: %w", path, err)
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: ping %q: %w", path, err)
	}

	db := &DB{conn: conn}
	if err := migrate(db); err != nil {
		conn.Close()
		return nil, fmt.Errorf("store: migrate %q: %w", path, err)
	}

	return db, nil
}

// OpenTest opens an in-memory database with migrations applied. The caller is
// responsible for closing the returned DB.
func OpenTest() (*DB, error) {
	return Open(":memory:")
}

// Conn returns the underlying *sql.DB.
func (db *DB) Conn() *sql.DB { return db.conn }

// Close closes the database connection.
func (db *DB) Close() error { return db.conn.Close() }

// Exec is a convenience wrapper around db.conn.ExecContext.
func (db *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.conn.ExecContext(ctx, query, args...)
}

// QueryRow is a convenience wrapper.
func (db *DB) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return db.conn.QueryRowContext(ctx, query, args...)
}

// Query is a convenience wrapper.
func (db *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.conn.QueryContext(ctx, query, args...)
}

// WithTransaction executes fn inside a single SQLite transaction. If fn returns
// an error the transaction is rolled back; otherwise it commits.
func (db *DB) WithTransaction(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// WithTestDB creates an in-memory database, runs fn, and closes the database.
// It fails the test if setup fails and propagates fn's error.
func WithTestDB(fn func(db *DB) error) error {
	db, err := OpenTest()
	if err != nil {
		return fmt.Errorf("WithTestDB: open: %w", err)
	}
	defer db.Close()
	return fn(db)
}

// NowUTC returns the current time in UTC truncated to millisecond precision,
// matching SQLite strftime('%Y-%m-%dT%H:%M:%fZ').
func NowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
