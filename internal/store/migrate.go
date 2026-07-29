package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate ensures the schema_version table exists and applies any pending
// numbered migrations in order inside a single transaction.
func migrate(db *DB) error {
	return migrateWithSource(db, migrationFS)
}

// migrateWithSource is the production migration runner with an injected source
// seam used to prove upgrade and rollback behavior against controlled fixtures.
func migrateWithSource(db *DB, source fs.FS) error {
	ctx := context.Background()

	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrate: create schema_version: %w", err)
	}

	current, err := currentVersion(db)
	if err != nil {
		return err
	}

	entries, err := fs.ReadDir(source, "migrations")
	if err != nil {
		return fmt.Errorf("migrate: read embedded migrations: %w", err)
	}

	type migration struct {
		version int
		name    string
	}
	var pending []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(e.Name())
		if err != nil {
			return fmt.Errorf("migrate: bad migration file %q: %w", e.Name(), err)
		}
		if version > current {
			pending = append(pending, migration{version: version, name: e.Name()})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	for _, m := range pending {
		sqlBytes, err := fs.ReadFile(source, "migrations/"+m.name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", m.name, err)
		}

		err = db.WithTransaction(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
				return fmt.Errorf("migrate: apply %s: %w", m.name, err)
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
				m.version, NowUTC(),
			); err != nil {
				return fmt.Errorf("migrate: record %s: %w", m.name, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func currentVersion(db *DB) (int, error) {
	ctx := context.Background()
	var version int
	err := db.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("migrate: read current version: %w", err)
	}
	return version, nil
}

// parseMigrationVersion extracts the leading integer from a filename like "001_initial.sql".
func parseMigrationVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("no version prefix")
	}
	return strconv.Atoi(parts[0])
}
