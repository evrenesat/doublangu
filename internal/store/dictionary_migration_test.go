package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
)

// TestMigration013_DictionaryExploreRehearsal proves the dictionary migration
// against a populated version-012 database: job, dependency, and relay rows
// survive byte-for-field together with the 012 ailocals column, foreign keys
// and integrity hold, the new reader.dictionary.v1 job type is admitted, and
// unknown job types stay rejected.
func TestMigration013_DictionaryExploreRehearsal(t *testing.T) {
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

	through012 := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql", "007_progressive_reader.sql",
		"008_analysis_provider_pipeline.sql", "009_stage_cache_provider_identity.sql",
		"010_attempt_truncation_flags.sql", "011_llm_relay.sql", "012_ailocals_presence.sql",
	} {
		through012["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through012); err != nil {
		t.Fatalf("apply migrations through 012: %v", err)
	}
	ctx := context.Background()

	insertJob := func(id, jobType, state string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, priority, attempt_count, max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at, progress_percent, error_code, created_at, updated_at, started_at, completed_at, ailocals_result_sha256) VALUES (?, ?, 'server', 'test_owner', ?, ?, 'hash-'+?, '{"v":1}', ?, 3, 1, 3, '2026-01-01T00:00:00.000Z', 'worker-1', 'tok', '2026-06-01T00:00:00.000Z', 10, '', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '', 'digest-'+?)`, id, jobType, "owner-"+id, id, id, state, id); err != nil {
			t.Fatalf("insert job %s: %v", id, err)
		}
	}
	insertJob("01J000000000000000000000D1", "reader.analysis.v2", "queued")
	insertJob("01J000000000000000000000D2", "llm.relay.v1", "succeeded")
	insertJob("01J000000000000000000000D3", "tts.chatterbox.v3", "leased")
	if _, err := db.Exec(ctx, `INSERT INTO job_dependency (job_id, dependency_job_id) VALUES ('01J000000000000000000000D1', '01J000000000000000000000D2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO llm_relay_result (job_id, input_hash, operation, result_json, result_hash, created_at) VALUES ('01J000000000000000000000D2', 'req-hash', 'chat_completion', '{"ok":true}', 'res-hash', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO speech_worker (id, name, protocol_version, token_hash, last_seen_at, capabilities_json, software_version) VALUES ('01J000000000000000000000E1', 'mac', 'speech-worker.v1', 'tok', '2026-09-01T00:00:00.000Z', '[]', '0.1')`); err != nil {
		t.Fatal(err)
	}

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

	counts := func(table string) int {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return count
	}
	jobCount, depCount, relayCount := counts("job"), counts("job_dependency"), counts("llm_relay_result")

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("apply migration 013: %v", err)
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
	if strings.Join(jobsBefore, "\n") != strings.Join(snapshot(`SELECT `+jobColumns+` FROM job ORDER BY id`), "\n") {
		t.Fatal("job rows are not byte-for-field equivalent after migration")
	}
	if strings.Join(depsBefore, "\n") != strings.Join(snapshot(`SELECT job_id, dependency_job_id FROM job_dependency ORDER BY job_id, dependency_job_id`), "\n") {
		t.Fatal("dependency rows changed after migration")
	}
	if strings.Join(relayBefore, "\n") != strings.Join(snapshot(`SELECT job_id, input_hash, operation, result_json, result_hash, created_at FROM llm_relay_result ORDER BY job_id`), "\n") {
		t.Fatal("relay result rows changed after migration")
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

	insertProbe := func(id, jobType string) error {
		_, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES (?, ?, 'server', 'dictionary_entry', 'entry-1', ?, 'abc', '{}', 'queued')`, id, jobType, "probe:"+id)
		return err
	}
	if err := insertProbe("01J000000000000000000000F1", "reader.dictionary.v1"); err != nil {
		t.Fatalf("dictionary job insert: %v", err)
	}
	if err := insertProbe("01J000000000000000000000F2", "reader.analysis.v2"); err != nil {
		t.Fatalf("legacy job insert: %v", err)
	}
	if err := insertProbe("01J000000000000000000000F3", "bogus.v1"); err == nil {
		t.Fatal("unknown job type must still be rejected")
	}

	// Dictionary placeholder rows are admitted; the document fields enforce
	// the null/populated-together invariant.
	if _, err := db.Exec(ctx, `INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, last_job_id) VALUES ('01J000000000000000000000G1', 'nl', 'en', 'word', 'bank', 'bank', '01J000000000000000000000F1')`); err != nil {
		t.Fatalf("placeholder dictionary entry: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, document_json) VALUES ('01J000000000000000000000G2', 'nl', 'en', 'word', 'hok', 'hok', '{"v":1}')`); err == nil {
		t.Fatal("document fields must be null or populated together")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form) VALUES ('01J000000000000000000000G3', 'nl', 'en', 'word', 'bank', 'bank')`); err == nil {
		t.Fatal("dictionary key uniqueness must hold")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form) VALUES ('01J000000000000000000000G4', 'nl', 'en', 'idiom', 'x', 'x')`); err == nil {
		t.Fatal("idiom lookup_kind must be rejected; expressions collapse to 'expression'")
	}
	var referencedJob string
	if err := db.QueryRow(ctx, `SELECT j.job_type FROM dictionary_entry d JOIN job j ON j.id = d.last_job_id WHERE d.id = '01J000000000000000000000G1'`).Scan(&referencedJob); err != nil {
		t.Fatalf("last_job_id join: %v", err)
	}
	if referencedJob != "reader.dictionary.v1" {
		t.Fatalf("referenced job type = %q", referencedJob)
	}
}

// TestMigration013_RollbackOnFailure proves an injected failure inside the
// migration leaves the populated 012 database untouched.
func TestMigration013_RollbackOnFailure(t *testing.T) {
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

	through012 := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql", "007_progressive_reader.sql",
		"008_analysis_provider_pipeline.sql", "009_stage_cache_provider_identity.sql",
		"010_attempt_truncation_flags.sql", "011_llm_relay.sql", "012_ailocals_presence.sql",
	} {
		through012["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through012); err != nil {
		t.Fatalf("apply migrations through 012: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES ('01J000000000000000000000H1', 'llm.relay.v1', 'server', 'relay', 'req-1', 'key-1', 'hash', '{}', 'succeeded')`); err != nil {
		t.Fatal(err)
	}

	// A broken 013: the job rebuild succeeds but the final dictionary insert
	// violates its own CHECK, so the whole migration transaction must roll
	// back and restore both child tables with the original job table.
	broken013 := checkedInMigrationString(t, "013_dictionary_explore.sql") +
		"\nINSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form, normalized_lookup_form, document_json) VALUES ('01J000000000000000000000Z9', 'nl', 'en', 'word', 'broken', 'broken', '{}');\n"
	source := fstest.MapFS{}
	for _, name := range migrationNames() {
		source["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	source["migrations/013_dictionary_explore.sql"] = &fstest.MapFile{Data: []byte(broken013)}
	if err := migrateWithSource(db, source); err == nil {
		t.Fatal("broken migration 013 must fail")
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE id = '01J000000000000000000000H1' AND job_type = 'llm.relay.v1' AND ailocals_result_sha256 = ''`).Scan(&count); err != nil {
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
	if version != 12 {
		t.Fatalf("failed 013 must leave the database at version 12, got %d", version)
	}
}

func checkedInMigrationString(t *testing.T, name string) string {
	t.Helper()
	return string(checkedInMigration(t, name))
}

func migrationNames() []string {
	return []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql", "007_progressive_reader.sql",
		"008_analysis_provider_pipeline.sql", "009_stage_cache_provider_identity.sql",
		"010_attempt_truncation_flags.sql", "011_llm_relay.sql", "012_ailocals_presence.sql",
		"013_dictionary_explore.sql",
	}
}
