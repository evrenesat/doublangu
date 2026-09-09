package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
)

// TestMigration015_SentenceTranslationRehearsal proves the sentence
// translation migration against a populated version-014 database: job,
// dependency, and relay rows survive byte-for-field together with the 012
// ailocals column and the 013 dictionary rows, existing articles and their
// sentence anchors survive untouched, foreign keys and integrity hold, the
// new reader.sentence_translation.v1 job type is admitted, unknown job types
// stay rejected, and the sentence_translation foreign keys behave.
func TestMigration015_SentenceTranslationRehearsal(t *testing.T) {
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

	applyMigrationsThrough(t, db, "014_prompt_profiles_and_run_operations.sql")
	ctx := context.Background()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %s: %v", query, err)
		}
	}
	insertJob := func(id, jobType, state string) {
		t.Helper()
		exec(`INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, priority, attempt_count, max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at, progress_percent, error_code, created_at, updated_at, started_at, completed_at, ailocals_result_sha256) VALUES (?, ?, 'server', 'test_owner', ?, ?, 'hash-'+?, '{"v":1}', ?, 3, 1, 3, '2026-01-01T00:00:00.000Z', 'worker-1', 'tok', '2026-06-01T00:00:00.000Z', 10, '', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '', 'digest-'+?)`, id, jobType, "owner-"+id, id, id, state, id)
	}
	insertJob("01J000000000000000000000D1", "reader.analysis.v2", "queued")
	insertJob("01J000000000000000000000D2", "llm.relay.v1", "succeeded")
	insertJob("01J000000000000000000000D3", "reader.dictionary.v1", "succeeded")
	exec(`INSERT INTO job_dependency (job_id, dependency_job_id) VALUES ('01J000000000000000000000D1', '01J000000000000000000000D2')`)
	exec(`INSERT INTO llm_relay_result (job_id, input_hash, operation, result_json, result_hash, created_at) VALUES ('01J000000000000000000000D2', 'req-hash', 'chat_completion', '{"ok":true}', 'res-hash', '2026-01-01T00:00:00.000Z')`)
	exec(`INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, last_job_id) VALUES ('01J000000000000000000000G1', 'nl', 'en', 'word', 'bank', 'bank', '01J000000000000000000000D3')`)

	// Populated article with one stored sentence anchor.
	exec(`INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES ('01J00000000000000000000ART1', 'Fixture', 'nl', 'en', 'ready')`)
	exec(`INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J00000000000000000000BLK1', '01J00000000000000000000ART1', 0, 'paragraph', 'De bank.')`)
	exec(`INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES ('01J00000000000000000000SEN1', '01J00000000000000000000BLK1', 0, 0, 20, 'Zij zit op een bank.', 'hash-sen-1')`)

	jobColumns := `id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, priority, attempt_count, max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at, progress_percent, error_code, created_at, updated_at, started_at, completed_at, ailocals_result_sha256`
	snapshot := func(query string) []string {
		t.Helper()
		rows, err := db.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			var parts []string
			for _, value := range values {
				parts = append(parts, strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(stringify(value)), "\x00", ""), "|", "/"))
			}
			out = append(out, strings.Join(parts, "|"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	jobsBefore := snapshot(`SELECT ` + jobColumns + ` FROM job ORDER BY id`)
	depsBefore := snapshot(`SELECT job_id, dependency_job_id FROM job_dependency ORDER BY job_id, dependency_job_id`)
	relayBefore := snapshot(`SELECT job_id, input_hash, operation, result_json, result_hash, created_at FROM llm_relay_result ORDER BY job_id`)
	dictionaryBefore := snapshot(`SELECT id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, last_job_id FROM dictionary_entry ORDER BY id`)
	sentencesBefore := snapshot(`SELECT id, article_block_id, sentence_index, source_text, source_hash FROM article_sentence ORDER BY id`)

	counts := func(table string) int {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return count
	}
	jobCount, depCount, relayCount, dictionaryCount, sentenceCount := counts("job"), counts("job_dependency"), counts("llm_relay_result"), counts("dictionary_entry"), counts("article_sentence")

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("apply migration 015: %v", err)
	}

	if got := counts("job"); got != jobCount {
		t.Fatalf("job count changed: before=%d after=%d", jobCount, got)
	}
	if got := counts("job_dependency"); got != depCount {
		t.Fatalf("dependency count changed: before=%d after=%d", depCount, got)
	}
	if got := counts("llm_relay_result"); got != relayCount {
		t.Fatalf("relay result count changed: before=%d after=%d", relayCount, got)
	}
	if got := counts("dictionary_entry"); got != dictionaryCount {
		t.Fatalf("dictionary count changed: before=%d after=%d", dictionaryCount, got)
	}
	if got := counts("article_sentence"); got != sentenceCount {
		t.Fatalf("sentence count changed: before=%d after=%d", sentenceCount, got)
	}
	if strings.Join(jobsBefore, "\n") != strings.Join(snapshot(`SELECT `+jobColumns+` FROM job ORDER BY id`), "\n") {
		t.Fatal("job rows are not byte-for-field equivalent after migration")
	}
	if strings.Join(depsBefore, "\n") != strings.Join(snapshot(`SELECT job_id, dependency_job_id FROM job_dependency ORDER BY job_id, dependency_job_id`), "\n") {
		t.Fatal("dependency rows changed after migration")
	}
	if strings.Join(relayBefore, "\n") != strings.Join(snapshot(`SELECT job_id, input_hash, operation, result_json, result_hash, created_at FROM llm_relay_result ORDER BY job_id`), "\n") {
		t.Fatal("relay result rows changed after migration")
	}
	if strings.Join(dictionaryBefore, "\n") != strings.Join(snapshot(`SELECT id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, last_job_id FROM dictionary_entry ORDER BY id`), "\n") {
		t.Fatal("dictionary rows changed after migration")
	}
	if strings.Join(sentencesBefore, "\n") != strings.Join(snapshot(`SELECT id, article_block_id, sentence_index, source_text, source_hash FROM article_sentence ORDER BY id`), "\n") {
		t.Fatal("sentence anchors changed after migration")
	}

	var backupTables int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE '%_backup_015'`).Scan(&backupTables); err != nil {
		t.Fatal(err)
	}
	if backupTables != 0 {
		t.Fatalf("%d backup tables left behind", backupTables)
	}

	rows, err := db.Query(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	violations := 0
	for rows.Next() {
		violations++
	}
	rows.Close()
	if violations != 0 {
		t.Fatalf("foreign_key_check found %d violations", violations)
	}
	var integrity string
	if err := db.QueryRow(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}

	// The widened job CHECK admits the new sentence job type while legacy
	// types keep working and unknown types stay rejected.
	insertProbe := func(id, jobType string) error {
		_, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES (?, ?, 'server', 'sentence_translation', 'sen-1', ?, 'abc', '{}', 'queued')`, id, jobType, "probe:"+id)
		return err
	}
	if err := insertProbe("01J000000000000000000000F1", "reader.sentence_translation.v1"); err != nil {
		t.Fatalf("sentence job insert: %v", err)
	}
	if err := insertProbe("01J000000000000000000000F2", "reader.dictionary.v1"); err != nil {
		t.Fatalf("legacy dictionary job insert: %v", err)
	}
	if err := insertProbe("01J000000000000000000000F3", "bogus.v1"); err == nil {
		t.Fatal("unknown job type must still be rejected")
	}

	// Existing articles start with no saved translation; the row keys to the
	// exact stored anchor and rejects foreign anchors and run pointers.
	exec(`INSERT INTO sentence_translation (sentence_id, source_hash, target_language, translation_text, result_hash, provenance_json, last_job_id) VALUES ('01J00000000000000000000SEN1', 'hash-sen-1', 'en', 'She sits on a bench.', 'res-hash', '{"op":"test"}', '01J000000000000000000000F1')`)
	if _, err := db.Exec(ctx, `INSERT INTO sentence_translation (sentence_id, source_hash, target_language) VALUES ('01J00000000000000000000NOPE', 'hash-x', 'en')`); err == nil {
		t.Fatal("foreign sentence_id must be rejected")
	}
	if _, err := db.Exec(ctx, `INSERT INTO sentence_translation (sentence_id, source_hash, target_language, last_run_id) VALUES ('01J00000000000000000000SEN1', 'hash-sen-1', 'en', '01J00000000000000000000RUN9')`); err == nil {
		t.Fatal("foreign last_run_id must be rejected")
	}

	// Deleting the anchor cascades the saved translation; deleting the job
	// nulls the pointer instead.
	exec(`INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J00000000000000000000BLK2', '01J00000000000000000000ART1', 1, 'paragraph', 'Het regent.')`)
	exec(`INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES ('01J00000000000000000000SEN2', '01J00000000000000000000BLK2', 0, 0, 11, 'Het regent.', 'hash-sen-2')`)
	exec(`INSERT INTO sentence_translation (sentence_id, source_hash, target_language, translation_text) VALUES ('01J00000000000000000000SEN2', 'hash-sen-2', 'en', 'It rains.')`)
	exec(`DELETE FROM article_sentence WHERE id = '01J00000000000000000000SEN2'`)
	if got := counts("sentence_translation"); got != 1 {
		t.Fatalf("anchor delete must cascade its translation, rows = %d", got)
	}
	exec(`DELETE FROM job WHERE id = '01J000000000000000000000F1'`)
	var lastJob sql.NullString
	if err := db.QueryRow(ctx, `SELECT last_job_id FROM sentence_translation WHERE sentence_id = '01J00000000000000000000SEN1'`).Scan(&lastJob); err != nil {
		t.Fatal(err)
	}
	if lastJob.Valid {
		t.Fatal("job delete must null the translation pointer")
	}
	var kept string
	if err := db.QueryRow(ctx, `SELECT translation_text FROM sentence_translation WHERE sentence_id = '01J00000000000000000000SEN1'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != "She sits on a bench." {
		t.Fatalf("translation lost after job delete: %q", kept)
	}
}

// TestMigration015_RollbackOnFailure proves an injected failure inside the
// migration leaves the populated 014 database untouched.
func TestMigration015_RollbackOnFailure(t *testing.T) {
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

	applyMigrationsThrough(t, db, "014_prompt_profiles_and_run_operations.sql")
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES ('01J000000000000000000000H1', 'reader.dictionary.v1', 'server', 'dictionary_entry', 'entry-1', 'key-1', 'hash', '{}', 'succeeded')`); err != nil {
		t.Fatal(err)
	}

	// A broken 015: the job rebuild succeeds but the final translation insert
	// references a missing anchor, so the whole migration transaction must
	// roll back and restore both child tables with the original job table.
	broken015 := checkedInMigrationString(t, "015_sentence_translation.sql") +
		"\nINSERT INTO sentence_translation (sentence_id, source_hash, target_language) VALUES ('01J00000000000000000000NOPE', 'hash-x', 'en');\n"
	source := fstest.MapFS{}
	for _, name := range migrationNamesUpTo("015_sentence_translation.sql") {
		source["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	source["migrations/015_sentence_translation.sql"] = &fstest.MapFile{Data: []byte(broken015)}
	if err := migrateWithSource(db, source); err == nil {
		t.Fatal("broken migration 015 must fail")
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE id = '01J000000000000000000000H1' AND job_type = 'reader.dictionary.v1' AND ailocals_result_sha256 = ''`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("original job row must survive the failed migration untouched")
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('job_dependency', 'llm_relay_result')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("child tables missing after rollback: %d", count)
	}
	if _, err := db.Exec(ctx, `INSERT INTO job_dependency (job_id, dependency_job_id) VALUES ('01J000000000000000000000H1', '01J000000000000000000000H1')`); err == nil {
		t.Fatal("job_dependency self-reference must still be rejected after rollback")
	} else if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "check") {
		t.Fatalf("unexpected rollback probe error: %v", err)
	}
	var version int
	if err := db.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Fatalf("failed 015 must leave the database at version 14, got %d", version)
	}
}
