package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"
)

func TestOpenInMemoryCreatesTables(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	rows, err := db.Query(ctx, "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// Verify every foundation table exists.
	found := make(map[string]bool)
	for _, t := range tables {
		found[t] = true
	}
	expected := []string{"device", "outbox", "owner", "plugin_settings", "schema_version", "session"}
	for _, name := range expected {
		if !found[name] {
			t.Errorf("missing table %q (found: %v)", name, tables)
		}
	}
}

func TestMigrationVersionRecorded(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatalf("OpenTest: %v", err)
	}
	defer db.Close()

	var version int
	err = db.QueryRow(context.Background(), "SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("version query: %v", err)
	}
	if version != 1 {
		t.Errorf("expected version 1, got %d", version)
	}
}

func TestMigrationFreshInMemoryAlwaysApplies(t *testing.T) {
	// In-memory databases are always fresh; each OpenTest runs migration from scratch.
	db, err := OpenTest()
	if err != nil {
		t.Fatalf("first OpenTest: %v", err)
	}
	db.Close()

	db2, err := OpenTest()
	if err != nil {
		t.Fatalf("second OpenTest: %v", err)
	}
	defer db2.Close()

	var count int
	err = db2.QueryRow(context.Background(), "SELECT COUNT(*) FROM schema_version").Scan(&count)
	if err != nil {
		t.Fatalf("version count: %v", err)
	}
	// Each in-memory OpenTest starts fresh — migration runs once per open.
	if count != 1 {
		t.Errorf("expected 1 migration record, got %d", count)
	}
}

func TestWithTransactionCommits(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		return db.WithTransaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO device (id, name) VALUES (?, ?)", "dev-1", "test device")
			return err
		})
	})
	if err != nil {
		t.Fatalf("WithTransaction: %v", err)
	}

	// Verify in a fresh DB that the test above actually committed.
	err = WithTestDB(func(db *DB) error {
		ctx := context.Background()
		return db.WithTransaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO device (id, name) VALUES (?, ?)", "dev-1", "test device")
			return err
		})
	})
	if err != nil {
		t.Fatalf("second WithTransaction: %v", err)
	}
}

func TestWithTransactionRollsBackOnError(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		rollbackErr := db.WithTransaction(ctx, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, "INSERT INTO device (id, name) VALUES (?, ?)", "should-rollback", "test")
			if execErr != nil {
				return execErr
			}
			return fmt.Errorf("forced rollback")
		})
		if rollbackErr == nil {
			t.Fatal("expected rollback error")
		}
		if rollbackErr.Error() != "forced rollback" {
			t.Errorf("unexpected rollback error: %v", rollbackErr)
		}
		// Verify the insert was rolled back.
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM device").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("expected 0 devices after rollback, got %d", count)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTestDB: %v", err)
	}
}

func TestWithTestDBIsolation(t *testing.T) {
	var firstCount, secondCount int
	if err := WithTestDB(func(db *DB) error {
		_, err := db.Exec(context.Background(), "INSERT INTO device (id, name) VALUES ('a', 'first')")
		if err != nil {
			return err
		}
		return db.QueryRow(context.Background(), "SELECT COUNT(*) FROM device").Scan(&firstCount)
	}); err != nil {
		t.Fatal(err)
	}
	if err := WithTestDB(func(db *DB) error {
		return db.QueryRow(context.Background(), "SELECT COUNT(*) FROM device").Scan(&secondCount)
	}); err != nil {
		t.Fatal(err)
	}
	if firstCount != 1 {
		t.Errorf("first db count = %d", firstCount)
	}
	if secondCount != 0 {
		t.Errorf("second db count = %d (want isolation)", secondCount)
	}
}

func TestOwnerTableConstraintSingleRow(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		_, err := db.Exec(ctx, "INSERT INTO owner (id, password_hash) VALUES (1, 'hash')")
		if err != nil {
			return err
		}
		// Second insert with different id must fail due to CHECK (id = 1).
		_, err = db.Exec(ctx, "INSERT INTO owner (id, password_hash) VALUES (2, 'hash2')")
		if err == nil {
			t.Fatal("expected constraint error for owner id != 1")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTestDB: %v", err)
	}
}

func TestSessionTableCRUD(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		_, err := db.Exec(ctx, "INSERT INTO session (token, expires_at, user_agent) VALUES ('tok', '2026-01-01T00:00:00.000Z', 'test')")
		if err != nil {
			return err
		}
		var tok string
		if err := db.QueryRow(ctx, "SELECT token FROM session WHERE token='tok'").Scan(&tok); err != nil {
			return err
		}
		if tok != "tok" {
			t.Errorf("expected tok, got %q", tok)
		}
		_, err = db.Exec(ctx, "DELETE FROM session WHERE token='tok'")
		if err != nil {
			return err
		}
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM session").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("expected 0 sessions after delete, got %d", count)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTestDB: %v", err)
	}
}

func TestFileBasedDBPersists(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = db.Exec(context.Background(), "INSERT INTO device (id, name) VALUES ('p1', 'persistent')")
	if err != nil {
		db.Close()
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	// Reopen and verify data persisted.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	var name string
	if err := db2.QueryRow(context.Background(), "SELECT name FROM device WHERE id='p1'").Scan(&name); err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "persistent" {
		t.Errorf("expected 'persistent', got %q", name)
	}
}

func TestFileBasedDBDoesNotReapplyMigrations(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRow(context.Background(), "SELECT COUNT(*) FROM schema_version").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 migration record, got %d (migrations should not reapply)", count)
	}
}

func TestMigrationUpgradeRetainsPopulatedOlderSchemaData(t *testing.T) {
	path := t.TempDir() + "/upgrade.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
		INSERT INTO schema_version (version, applied_at) VALUES (1, '2026-01-01T00:00:00.000Z');
		CREATE TABLE owner (id INTEGER PRIMARY KEY, password_hash TEXT NOT NULL);
		INSERT INTO owner (id, password_hash) VALUES (1, 'retained-hash');
	`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	db := &DB{conn: conn}
	source := fstest.MapFS{
		"migrations/001_initial.sql":    {Data: []byte("CREATE TABLE owner (id INTEGER PRIMARY KEY, password_hash TEXT NOT NULL);")},
		"migrations/002_owner_note.sql": {Data: []byte("ALTER TABLE owner ADD COLUMN note TEXT NOT NULL DEFAULT ''; ")},
	}
	if err := migrateWithSource(db, source); err != nil {
		conn.Close()
		t.Fatalf("upgrade: %v", err)
	}
	defer db.Close()
	var hash, note string
	if err := db.QueryRow(context.Background(), "SELECT password_hash, note FROM owner WHERE id = 1").Scan(&hash, &note); err != nil {
		t.Fatal(err)
	}
	if hash != "retained-hash" || note != "" {
		t.Fatalf("upgraded owner = hash=%q note=%q", hash, note)
	}
	var version int
	if err := db.QueryRow(context.Background(), "SELECT MAX(version) FROM schema_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema version = %d err=%v", version, err)
	}
}

func TestMigrationRollbackLeavesNoPartialSchemaDataOrVersion(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	failing := fstest.MapFS{
		"migrations/002_probe.sql": {Data: []byte(`
			CREATE TABLE migration_probe (value TEXT NOT NULL);
			INSERT INTO migration_probe (value) VALUES ('partial');
			THIS IS NOT SQL;
		`)},
	}
	if err := migrateWithSource(db, failing); err == nil {
		t.Fatal("failing migration unexpectedly succeeded")
	}
	var tableCount, versionCount int
	if err := db.QueryRow(context.Background(), "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='migration_probe'").Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), "SELECT COUNT(*) FROM schema_version WHERE version = 2").Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 || versionCount != 0 {
		t.Fatalf("rollback left table=%d version=%d", tableCount, versionCount)
	}

	corrected := fstest.MapFS{
		"migrations/002_probe.sql": {Data: []byte(`
			CREATE TABLE migration_probe (value TEXT NOT NULL);
			INSERT INTO migration_probe (value) VALUES ('complete');
		`)},
	}
	if err := migrateWithSource(db, corrected); err != nil {
		t.Fatalf("corrected migration: %v", err)
	}
	var value string
	if err := db.QueryRow(context.Background(), "SELECT value FROM migration_probe").Scan(&value); err != nil || value != "complete" {
		t.Fatalf("corrected value = %q err=%v", value, err)
	}
}

func TestFileDatabaseUsesWALForeignKeysBusyTimeoutAndCurrentVersion(t *testing.T) {
	db, err := Open(t.TempDir() + "/pragmas.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var journal string
	var foreignKeys, busyTimeout, version int
	if err := db.QueryRow(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT MAX(version) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || foreignKeys != 1 || busyTimeout != 5000 || version != 1 {
		t.Fatalf("journal=%q foreign_keys=%d busy_timeout=%d version=%d", journal, foreignKeys, busyTimeout, version)
	}
}
