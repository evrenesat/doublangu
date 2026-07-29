package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"

	"doublangu/internal/library"
)

const (
	migrationLibraryID     = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	migrationWorkID        = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	migrationEditionID     = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	migrationChapterID     = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	migrationSourceAssetID = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
)

func checkedInMigration(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatalf("read checked-in migration %s: %v", name, err)
	}
	return contents
}

func openPopulatedV1DB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	db := &DB{conn: conn}
	t.Cleanup(func() { _ = db.Close() })

	versionOne := fstest.MapFS{
		"migrations/001_initial.sql": {Data: checkedInMigration(t, "001_initial.sql")},
	}
	if err := migrateWithSource(db, versionOne); err != nil {
		t.Fatalf("apply checked-in migration 001: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Exec(ctx, "INSERT INTO owner (id, password_hash) VALUES (1, 'cp8-hash')"); err != nil {
		t.Fatalf("insert CP8 owner: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO session (token, expires_at) VALUES ('cp8-session', '2026-12-31T00:00:00.000Z')"); err != nil {
		t.Fatalf("insert CP8 session: %v", err)
	}
	return db
}

func assertCP8Rows(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	var passwordHash, token string
	if err := db.QueryRow(ctx, "SELECT password_hash FROM owner WHERE id = 1").Scan(&passwordHash); err != nil || passwordHash != "cp8-hash" {
		t.Fatalf("CP8 owner = %q err=%v", passwordHash, err)
	}
	if err := db.QueryRow(ctx, "SELECT token FROM session WHERE token = 'cp8-session'").Scan(&token); err != nil || token != "cp8-session" {
		t.Fatalf("CP8 session = %q err=%v", token, err)
	}
}

func assertMigration002Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	expected := []string{
		"library", "work", "edition", "chapter", "source_asset",
		"idx_work_library", "idx_edition_work", "idx_chapter_edition", "idx_source_asset_chapter",
	}
	for _, name := range expected {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find schema object %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("schema object %s count = %d, want 1", name, count)
		}
	}
}

func assertMigrationVersion(t *testing.T, db *DB, want int) {
	t.Helper()
	ctx := context.Background()
	var version, count int
	if err := db.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil || version != want {
		t.Fatalf("current version = %d err=%v, want %d", version, err, want)
	}
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM schema_version WHERE version = ?", want).Scan(&count); err != nil || count != 1 {
		t.Fatalf("version %d record count = %d err=%v, want 1", want, count, err)
	}
}

func insertMigrationParents(ctx context.Context, db *DB) error {
	if _, err := db.Exec(ctx,
		"INSERT INTO library (id, name, source_language, target_language) VALUES (?, 'Test', 'nl', 'en')", migrationLibraryID); err != nil {
		return err
	}
	if _, err := db.Exec(ctx,
		"INSERT INTO work (id, library_id, title) VALUES (?, ?, 'Test Work')", migrationWorkID, migrationLibraryID); err != nil {
		return err
	}
	_, err := db.Exec(ctx,
		"INSERT INTO edition (id, work_id, name, language) VALUES (?, ?, 'First', 'nl')", migrationEditionID, migrationWorkID)
	return err
}

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
	expected := []string{"chapter", "device", "edition", "library", "outbox", "owner", "plugin_settings", "schema_version", "session", "source_asset", "work"}
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
	if version != 2 {
		t.Errorf("expected version 2, got %d", version)
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
	// Each in-memory OpenTest starts fresh — all migrations run once per open.
	if count != 2 {
		t.Errorf("expected 2 migration records, got %d", count)
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
	if count != 2 {
		t.Errorf("expected 2 migration records, got %d (migrations should not reapply)", count)
	}
}

func TestMigrationRollbackLeavesNoPartialSchemaDataOrVersion(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	failing := fstest.MapFS{
		"migrations/003_probe.sql": {Data: []byte(`
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
	if err := db.QueryRow(context.Background(), "SELECT COUNT(*) FROM schema_version WHERE version = 3").Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 || versionCount != 0 {
		t.Fatalf("rollback left table=%d version=%d", tableCount, versionCount)
	}

	corrected := fstest.MapFS{
		"migrations/003_probe.sql": {Data: []byte(`
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

// --- Migration 002: Library metadata ---

func TestMigration002_ApplyCreatesLibraryTables(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	rows, err := db.Query(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('library','work','edition','chapter','source_asset') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) != 5 {
		t.Fatalf("expected 5 library tables, got %d: %v", len(tables), tables)
	}
}

func TestMigration002_ForeignKeyCascadeDelete(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()

		// Insert chain: library → work → edition → chapter → source_asset.
		if _, err := db.Exec(ctx,
			`INSERT INTO library (id, name, source_language, target_language) VALUES (?, 'Test', 'nl', 'en')`, migrationLibraryID); err != nil {
			return err
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO work (id, library_id, title) VALUES (?, ?, 'Test Work')`, migrationWorkID, migrationLibraryID); err != nil {
			return err
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO edition (id, work_id, name, language) VALUES (?, ?, 'First', 'nl')`, migrationEditionID, migrationWorkID); err != nil {
			return err
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, 'Ch1', 1, 0, 1000, 1000)`, migrationChapterID, migrationEditionID); err != nil {
			return err
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO source_asset (id, chapter_id, url, mime_type, size_bytes, sha256_hash, start_ms, end_ms, duration_ms) VALUES (?, ?, 'file:///audio.mp3', 'audio/mpeg', 1024, 'aa', 0, 1000, 1000)`, migrationSourceAssetID, migrationChapterID); err != nil {
			return err
		}

		// Delete library: all descendants must cascade.
		if _, err := db.Exec(ctx, `DELETE FROM library WHERE id = ?`, migrationLibraryID); err != nil {
			return err
		}

		counts := []struct{ table string }{
			{"library"}, {"work"}, {"edition"}, {"chapter"}, {"source_asset"},
		}
		for _, c := range counts {
			var count int
			if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM "+c.table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("cascade failed: %s has %d rows after library delete", c.table, count)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigration002_NonNegativeMillisecondChecks(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		if err := insertMigrationParents(ctx, db); err != nil {
			return err
		}

		// Negative start_ms must be rejected.
		_, err := db.Exec(ctx,
			`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, 'Ch1', 1, -1, 1000, 1001)`, migrationChapterID, migrationEditionID)
		if err == nil {
			t.Fatal("expected CHECK constraint error for negative start_ms")
		}

		// Negative end_ms must be rejected.
		_, err = db.Exec(ctx,
			`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, 'Ch2', 2, 0, -1, 0)`, migrationChapterID, migrationEditionID)
		if err == nil {
			t.Fatal("expected CHECK constraint error for negative end_ms")
		}

		// Negative duration_ms must be rejected.
		_, err = db.Exec(ctx,
			`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, 'Ch3', 3, 0, 1000, -1)`, migrationChapterID, migrationEditionID)
		if err == nil {
			t.Fatal("expected CHECK constraint error for negative duration_ms")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigration002_EndMsAtLeastStartMs(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		if err := insertMigrationParents(ctx, db); err != nil {
			return err
		}

		// end_ms < start_ms must be rejected.
		_, err := db.Exec(ctx,
			`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, 'Ch1', 1, 5000, 1000, 0)`, migrationChapterID, migrationEditionID)
		if err == nil {
			t.Fatal("expected CHECK constraint error for end_ms < start_ms on chapter")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigration002_SourceAssetEndMsAtLeastStartMs(t *testing.T) {
	err := WithTestDB(func(db *DB) error {
		ctx := context.Background()
		if err := insertMigrationParents(ctx, db); err != nil {
			return err
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES (?, ?, 'Ch1', 1, 0, 10000, 10000)`, migrationChapterID, migrationEditionID); err != nil {
			return err
		}

		// end_ms < start_ms must be rejected for source_asset.
		_, err := db.Exec(ctx,
			`INSERT INTO source_asset (id, chapter_id, url, start_ms, end_ms, duration_ms) VALUES (?, ?, 'file:///', 5000, 1000, 0)`, migrationSourceAssetID, migrationChapterID)
		if err == nil {
			t.Fatal("expected CHECK constraint error for end_ms < start_ms on source_asset")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigration002_UpgradeFromV1ToV2(t *testing.T) {
	db := openPopulatedV1DB(t)
	assertMigrationVersion(t, db, 1)

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("upgrade through embedded migrations: %v", err)
	}

	assertCP8Rows(t, db)
	assertMigration002Schema(t, db)
	assertMigrationVersion(t, db, 2)
}

func TestMetadataStoreCRUDOnCleanAndUpgradedDatabases(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		db, err := OpenTest()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		exerciseMetadataStoreCRUD(t, db)
	})
	t.Run("upgraded", func(t *testing.T) {
		db := openPopulatedV1DB(t)
		if err := migrateWithSource(db, migrationFS); err != nil {
			t.Fatalf("upgrade through checked-in migration: %v", err)
		}
		assertCP8Rows(t, db)
		exerciseMetadataStoreCRUD(t, db)
		assertCP8Rows(t, db)
	})
}

func exerciseMetadataStoreCRUD(t *testing.T, db *DB) {
	t.Helper()
	ctx, metadata := context.Background(), &library.Store{}
	err := db.WithTransaction(ctx, func(tx *sql.Tx) error {
		libraryRecord, err := library.NewLibrary("metadata", "NL-nl", "EN-us", "")
		if err != nil {
			return err
		}
		if err := metadata.CreateLibrary(ctx, tx, &libraryRecord); err != nil {
			return err
		}
		work, err := library.NewWork(libraryRecord.ID, "work", "author", "ebook", "")
		if err != nil {
			return err
		}
		if err := metadata.CreateWork(ctx, tx, &work); err != nil {
			return err
		}
		edition, err := library.NewEdition(work.ID, "edition", "NL-nl", "epub")
		if err != nil {
			return err
		}
		if err := metadata.CreateEdition(ctx, tx, &edition); err != nil {
			return err
		}
		chapter, err := library.NewChapter(edition.ID, "chapter", 1, 0, 1, 1)
		if err != nil {
			return err
		}
		if err := metadata.CreateChapter(ctx, tx, &chapter); err != nil {
			return err
		}
		asset, err := library.NewSourceAsset(chapter.ID, "file:///metadata.mp3", "audio/mpeg", 1, "hash", 0, 1, 1)
		if err != nil {
			return err
		}
		if err := metadata.CreateSourceAsset(ctx, tx, &asset); err != nil {
			return err
		}
		if _, err := metadata.GetLibrary(ctx, tx, libraryRecord.ID); err != nil {
			return err
		}
		if _, err := metadata.GetWork(ctx, tx, work.ID); err != nil {
			return err
		}
		if _, err := metadata.GetEdition(ctx, tx, edition.ID); err != nil {
			return err
		}
		if _, err := metadata.GetChapter(ctx, tx, chapter.ID); err != nil {
			return err
		}
		if _, err := metadata.GetSourceAsset(ctx, tx, asset.ID); err != nil {
			return err
		}
		if records, err := metadata.ListLibraries(ctx, tx); err != nil || len(records) != 1 {
			return fmt.Errorf("list libraries = %d, %w", len(records), err)
		}
		if records, err := metadata.ListWorksByLibrary(ctx, tx, libraryRecord.ID); err != nil || len(records) != 1 {
			return fmt.Errorf("list works = %d, %w", len(records), err)
		}
		if records, err := metadata.ListEditionsByWork(ctx, tx, work.ID); err != nil || len(records) != 1 {
			return fmt.Errorf("list editions = %d, %w", len(records), err)
		}
		if records, err := metadata.ListChaptersByEdition(ctx, tx, edition.ID); err != nil || len(records) != 1 {
			return fmt.Errorf("list chapters = %d, %w", len(records), err)
		}
		if records, err := metadata.ListSourceAssetsByChapter(ctx, tx, chapter.ID); err != nil || len(records) != 1 {
			return fmt.Errorf("list source assets = %d, %w", len(records), err)
		}
		libraryRecord.Name, work.Title, edition.Name, chapter.Title, asset.URL = "updated library", "updated work", "updated edition", "updated chapter", "file:///updated.mp3"
		for _, update := range []func() error{
			func() error { return metadata.UpdateLibrary(ctx, tx, &libraryRecord) },
			func() error { return metadata.UpdateWork(ctx, tx, &work) },
			func() error { return metadata.UpdateEdition(ctx, tx, &edition) },
			func() error { return metadata.UpdateChapter(ctx, tx, &chapter) },
			func() error { return metadata.UpdateSourceAsset(ctx, tx, &asset) },
		} {
			if err := update(); err != nil {
				return err
			}
		}
		updatedLibrary, err := metadata.GetLibrary(ctx, tx, libraryRecord.ID)
		if err != nil || updatedLibrary.Name != libraryRecord.Name {
			return fmt.Errorf("updated library = %#v, %w", updatedLibrary, err)
		}
		updatedWork, err := metadata.GetWork(ctx, tx, work.ID)
		if err != nil || updatedWork.Title != work.Title {
			return fmt.Errorf("updated work = %#v, %w", updatedWork, err)
		}
		updatedEdition, err := metadata.GetEdition(ctx, tx, edition.ID)
		if err != nil || updatedEdition.Name != edition.Name {
			return fmt.Errorf("updated edition = %#v, %w", updatedEdition, err)
		}
		updatedChapter, err := metadata.GetChapter(ctx, tx, chapter.ID)
		if err != nil || updatedChapter.Title != chapter.Title {
			return fmt.Errorf("updated chapter = %#v, %w", updatedChapter, err)
		}
		updatedAsset, err := metadata.GetSourceAsset(ctx, tx, asset.ID)
		if err != nil || updatedAsset.URL != asset.URL {
			return fmt.Errorf("updated source asset = %#v, %w", updatedAsset, err)
		}
		if err := metadata.DeleteLibrary(ctx, tx, libraryRecord.ID); err != nil {
			return err
		}
		for _, get := range []func() error{
			func() error { _, err := metadata.GetLibrary(ctx, tx, libraryRecord.ID); return err },
			func() error { _, err := metadata.GetWork(ctx, tx, work.ID); return err },
			func() error { _, err := metadata.GetEdition(ctx, tx, edition.ID); return err },
			func() error { _, err := metadata.GetChapter(ctx, tx, chapter.ID); return err },
			func() error { _, err := metadata.GetSourceAsset(ctx, tx, asset.ID); return err },
		} {
			var storeErr *library.Error
			if err := get(); !errors.As(err, &storeErr) || storeErr.Kind != library.KindNotFound {
				return fmt.Errorf("cascade error = %v", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("metadata CRUD: %v", err)
	}
}

func TestMigration002_RollbackLeavesNoLibraryTables(t *testing.T) {
	db := openPopulatedV1DB(t)
	migration002 := append([]byte(nil), checkedInMigration(t, "002_library.sql")...)
	failingSource := fstest.MapFS{
		"migrations/002_library.sql": {Data: append(migration002, "\nTHIS IS GUARANTEED TO FAIL;\n"...)},
	}

	if err := migrateWithSource(db, failingSource); err == nil {
		t.Fatal("controlled failing migration 002 unexpectedly succeeded")
	}

	ctx := context.Background()
	for _, name := range []string{
		"library", "work", "edition", "chapter", "source_asset",
		"idx_work_library", "idx_edition_work", "idx_chapter_edition", "idx_source_asset_chapter",
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find rollback schema object %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("rollback left schema object %s", name)
		}
	}
	assertCP8Rows(t, db)
	assertMigrationVersion(t, db, 1)

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("retry checked-in migration 002: %v", err)
	}
	assertCP8Rows(t, db)
	assertMigration002Schema(t, db)
	assertMigrationVersion(t, db, 2)
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
	if journal != "wal" || foreignKeys != 1 || busyTimeout != 5000 || version != 2 {
		t.Fatalf("journal=%q foreign_keys=%d busy_timeout=%d version=%d", journal, foreignKeys, busyTimeout, version)
	}
}
