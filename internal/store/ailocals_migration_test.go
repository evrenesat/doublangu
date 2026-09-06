package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
)

// TestMigration012_AilocalsPresenceRehearsal proves the additive ailocals
// presence migration against a realistic schema-11 fixture: enrolled workers,
// completed speech, an active relay result, and revoked credentials are
// preserved while the new columns carry safe defaults.
func TestMigration012_AilocalsPresenceRehearsal(t *testing.T) {
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

	through011 := fstest.MapFS{}
	for _, name := range migrationNamesThrough011() {
		through011["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through011); err != nil {
		t.Fatalf("apply migrations through 011: %v", err)
	}
	ctx := context.Background()

	if _, err := db.Exec(ctx, `INSERT INTO speech_worker (id, name, protocol_version, token_hash, last_seen_at, capabilities_json, software_version, llm_relay_capabilities_json, relay_last_seen_at) VALUES ('01J000000000000000000000B1', 'legacy mac', 'speech-worker.v1', 'tok-legacy', '2026-09-01T00:00:00.000Z', '[]', '0.1', '[]', '2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert worker: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO speech_worker (id, name, protocol_version, token_hash, revoked_at, last_seen_at, capabilities_json, software_version) VALUES ('01J000000000000000000000B2', 'retired mac', 'speech-worker.v1', 'tok-old', '2026-08-01T00:00:00.000Z', '2026-08-01T00:00:00.000Z', '[]', '0.1')`); err != nil {
		t.Fatalf("insert revoked worker: %v", err)
	}
	insertJob := func(id, jobType, state string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state, attempt_count, max_attempts, available_at, error_code, created_at, updated_at) VALUES (?, ?, 'macos', 'audio_render', ?, ?, 'hash-'+?, '{"v":1}', ?, 1, 3, '2026-01-01T00:00:00.000Z', '', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, id, jobType, "owner-"+id, id, id, state); err != nil {
			t.Fatalf("insert job %s: %v", id, err)
		}
	}
	insertJob("01J000000000000000000000A1", "tts.avspeech.v1", "succeeded")
	insertJob("01J000000000000000000000A2", "tts.chatterbox.v3", "queued")
	insertJob("01J000000000000000000000A3", "llm.relay.v1", "running")
	if _, err := db.Exec(ctx, `INSERT INTO llm_relay_result (job_id, input_hash, operation, result_json, result_hash) VALUES ('01J000000000000000000000A3', 'in-h', 'chat_completion', '{"ok":1}', 're-h')`); err != nil {
		t.Fatalf("insert relay result: %v", err)
	}

	all := fstest.MapFS{}
	for _, name := range migrationNamesThrough011() {
		all["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	all["migrations/012_ailocals_presence.sql"] = &fstest.MapFile{
		Data: checkedInMigration(t, "012_ailocals_presence.sql"),
	}
	if err := migrateWithSource(db, all); err != nil {
		t.Fatalf("apply migration 012: %v", err)
	}

	var presenceDefault, relayCaps string
	if err := db.QueryRow(ctx, `SELECT ailocals_presence_json, llm_relay_capabilities_json FROM speech_worker WHERE id = '01J000000000000000000000B1'`).Scan(&presenceDefault, &relayCaps); err != nil {
		t.Fatalf("worker row missing after migration: %v", err)
	}
	if presenceDefault != "{}" {
		t.Fatalf("ailocals_presence_json default = %q, want {}", presenceDefault)
	}
	if relayCaps != "[]" {
		t.Fatalf("legacy relay capabilities changed: %q", relayCaps)
	}
	var revokedAt string
	if err := db.QueryRow(ctx, `SELECT revoked_at FROM speech_worker WHERE id = '01J000000000000000000000B2'`).Scan(&revokedAt); err != nil {
		t.Fatalf("revoked worker lost: %v", err)
	}
	if revokedAt != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("revoked_at changed: %q", revokedAt)
	}

	var digest string
	var relayState string
	if err := db.QueryRow(ctx, `SELECT ailocals_result_sha256, state FROM job WHERE id = '01J000000000000000000000A3'`).Scan(&digest, &relayState); err != nil {
		t.Fatalf("relay job missing: %v", err)
	}
	if digest != "" {
		t.Fatalf("ailocals_result_sha256 default = %q, want empty", digest)
	}
	if relayState != "running" {
		t.Fatalf("relay job state changed: %q", relayState)
	}

	var renderState string
	if err := db.QueryRow(ctx, `SELECT state FROM job WHERE id = '01J000000000000000000000A1'`).Scan(&renderState); err != nil {
		t.Fatalf("speech job missing: %v", err)
	}
	if renderState != "succeeded" {
		t.Fatalf("speech job state changed: %q", renderState)
	}

	if _, err := db.Exec(ctx, `UPDATE speech_worker SET ailocals_presence_json = '{"capabilities":[{"id":"tts.apple-speech.v1","state":"ready","accepting":true,"active_jobs":0,"reason":null}],"protocol":"ailocals.v1"}' WHERE id = '01J000000000000000000000B1'`); err != nil {
		t.Fatalf("presence column not writable: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE job SET ailocals_result_sha256 = 'a' || 'b' WHERE id = '01J000000000000000000000A3'`); err != nil {
		t.Fatalf("digest column not writable: %v", err)
	}

	var integrity string
	if err := db.QueryRow(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity = %q", integrity)
	}
}

func migrationNamesThrough011() []string {
	return strings.Fields(
		"001_initial.sql 002_library.sql 003_media.sql 004_reader_mvp.sql" +
			" 005_audible_reader.sql 006_analysis_reliability.sql 007_progressive_reader.sql" +
			" 008_analysis_provider_pipeline.sql 009_stage_cache_provider_identity.sql" +
			" 010_attempt_truncation_flags.sql 011_llm_relay.sql",
	)
}
