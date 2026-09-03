package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func formatScalar(value any) string {
	return fmt.Sprintf("%v", value)
}

// TestMigration011_LLMRelayRehearsal proves the relay migration against a
// realistic pre-migration fixture: job and dependency rows survive
// byte-for-field, workers survive, foreign keys and integrity hold, and the
// new job type is admitted while the schema stays strict.
func TestMigration011_LLMRelayRehearsal(t *testing.T) {
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

	through010 := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql", "007_progressive_reader.sql",
		"008_analysis_provider_pipeline.sql", "009_stage_cache_provider_identity.sql",
		"010_attempt_truncation_flags.sql",
	} {
		through010["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through010); err != nil {
		t.Fatalf("apply migrations through 010: %v", err)
	}
	ctx := context.Background()

	insertJob := func(id, jobType, state string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, priority, attempt_count, max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at, progress_percent, error_code, created_at, updated_at, started_at, completed_at) VALUES (?, ?, 'macos', 'audio_render', ?, ?, 'hash-'+?, '{"v":1}', ?, 3, 1, 3, '2026-01-01T00:00:00.000Z', 'worker-1', 'tok', '2026-06-01T00:00:00.000Z', 10, '', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '')`, id, jobType, "owner-"+id, id, id, state); err != nil {
			t.Fatalf("insert job %s: %v", id, err)
		}
	}
	insertJob("01J000000000000000000000A1", "reader.analysis.v2", "queued")
	insertJob("01J000000000000000000000A2", "tts.avspeech.v1", "leased")
	insertJob("01J000000000000000000000A3", "tts.chatterbox.v3", "succeeded")
	if _, err := db.Exec(ctx, `INSERT INTO job_dependency (job_id, dependency_job_id) VALUES ('01J000000000000000000000A2', '01J000000000000000000000A3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO speech_worker (id, name, protocol_version, token_hash, last_seen_at, capabilities_json, software_version) VALUES ('01J000000000000000000000B1', 'mac', 'speech-worker.v1', 'tok', '2026-09-01T00:00:00.000Z', '[]', '0.1')`); err != nil {
		t.Fatal(err)
	}

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
	jobsBefore := snapshot(`SELECT id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, priority, attempt_count, max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at, progress_percent, error_code, created_at, updated_at, started_at, completed_at FROM job ORDER BY id`)
	depsBefore := snapshot(`SELECT job_id, dependency_job_id FROM job_dependency ORDER BY job_id, dependency_job_id`)

	var jobCount, depCount, workerCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job_dependency`).Scan(&depCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM speech_worker`).Scan(&workerCount); err != nil {
		t.Fatal(err)
	}

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("apply migration 011: %v", err)
	}

	var after int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != jobCount {
		t.Fatalf("job count changed: before=%d after=%d", jobCount, after)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job_dependency`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != depCount {
		t.Fatalf("dependency count changed: before=%d after=%d", depCount, after)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM speech_worker`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != workerCount {
		t.Fatalf("worker count changed: before=%d after=%d", workerCount, after)
	}
	jobsAfter := snapshot(`SELECT id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, priority, attempt_count, max_attempts, available_at, lease_owner, lease_token_hash, lease_expires_at, progress_percent, error_code, created_at, updated_at, started_at, completed_at FROM job ORDER BY id`)
	if strings.Join(jobsBefore, "\n") != strings.Join(jobsAfter, "\n") {
		t.Fatal("job rows are not byte-for-field equivalent after migration")
	}
	depsAfter := snapshot(`SELECT job_id, dependency_job_id FROM job_dependency ORDER BY job_id, dependency_job_id`)
	if strings.Join(depsBefore, "\n") != strings.Join(depsAfter, "\n") {
		t.Fatal("dependency rows changed after migration")
	}

	var fkViolations int
	rows, err := db.Query(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		fkViolations++
	}
	_ = rows.Close()
	if fkViolations != 0 {
		t.Fatalf("foreign_key_check found %d violations", fkViolations)
	}
	var integrity string
	if err := db.QueryRow(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}

	// The new relay job type inserts; the old types still validate.
	insertProbe := func(id, jobType string) error {
		_, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES (?, ?, 'macos', 'llm_relay', 'req-1', ?, 'abc', '{}', 'queued')`, id, jobType, "probe:"+id)
		return err
	}
	if err := insertProbe("01J000000000000000000000C1", "llm.relay.v1"); err != nil {
		t.Fatalf("relay job insert: %v", err)
	}
	if err := insertProbe("01J000000000000000000000C2", "tts.avspeech.v1"); err != nil {
		t.Fatalf("legacy job insert: %v", err)
	}
	if err := insertProbe("01J000000000000000000000C3", "bogus.v1"); err == nil {
		t.Fatal("unknown job type must still be rejected")
	}
	// Old relay-less workers keep working; new presence columns default.
	var relayCaps, relaySeen string
	if err := db.QueryRow(ctx, `SELECT llm_relay_capabilities_json, relay_last_seen_at FROM speech_worker WHERE id = '01J000000000000000000000B1'`).Scan(&relayCaps, &relaySeen); err != nil {
		t.Fatal(err)
	}
	if relayCaps != "[]" || relaySeen != "" {
		t.Fatalf("worker relay defaults wrong: %q %q", relayCaps, relaySeen)
	}
	var resultTable string
	if err := db.QueryRow(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'llm_relay_result'`).Scan(&resultTable); err != nil {
		t.Fatalf("llm_relay_result missing: %v", err)
	}
}

func stringify(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return formatScalar(typed)
	}
}
