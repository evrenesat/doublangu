package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
)

// applyMigrationsThrough applies every checked-in migration up to and
// including the given version using the injected source seam.
func applyMigrationsThrough(t *testing.T, db *DB, version string) {
	t.Helper()
	source := fstest.MapFS{}
	for _, name := range migrationNamesUpTo(version) {
		source["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, source); err != nil {
		t.Fatalf("apply migrations through %s: %v", version, err)
	}
}

func migrationNamesUpTo(version string) []string {
	all := []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql", "007_progressive_reader.sql",
		"008_analysis_provider_pipeline.sql", "009_stage_cache_provider_identity.sql",
		"010_attempt_truncation_flags.sql", "011_llm_relay.sql", "012_ailocals_presence.sql",
		"013_dictionary_explore.sql", "014_prompt_profiles_and_run_operations.sql",
	}
	for index, name := range all {
		if name == version {
			return all[:index+1]
		}
	}
	return all
}

// TestMigration014_PromptProfilesRehearsal proves migration 014 against a
// populated version-013 database: the Explore binding is copied from the
// translation binding exactly once, the run operation/subject/phase columns
// default legacy rows correctly, the attempt error_phase defaults empty, and
// the prompt tables enforce their identity, immutability, and selection
// rules together with foreign-key integrity.
func TestMigration014_PromptProfilesRehearsal(t *testing.T) {
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

	applyMigrationsThrough(t, db, "013_dictionary_explore.sql")
	ctx := context.Background()

	// Populated 013 state: one profile with both stage bindings, articles
	// with terminal and in-flight analysis runs, and one dictionary entry.
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %s: %v", strings.Join(strings.Fields(query)[:4], " "), err)
		}
	}
	mustExec(`INSERT INTO analysis_pipeline_profile (id, name) VALUES ('profile-1', 'Main')`)
	mustExec(`INSERT INTO analysis_pipeline_binding (profile_id, stage_id, provider_id, model_id, options_json, options_hash)
		VALUES ('profile-1', 'linguistic_analysis', 'codex-app-server', 'model-a', '{"reasoning_effort":"low"}', 'hash-linguistic')`)
	mustExec(`INSERT INTO analysis_pipeline_binding (profile_id, stage_id, provider_id, model_id, options_json, options_hash)
		VALUES ('profile-1', 'translation', 'codex-app-server', 'model-a', '{"reasoning_effort":"medium"}', 'hash-translation')`)
	mustExec(`INSERT INTO article (id, title, source_language, target_language, enrichment_status)
		VALUES ('article-1', 'Een titel', 'nl', 'en', 'ready')`)
	mustExec(`INSERT INTO analysis_run (id, article_id, job_id, attempt_count, content_hash, contract_version,
		prompt_version, requested_model, requested_effort, provider_id, started_at, status)
		VALUES ('run-succeeded', 'article-1', 'job-1', 1, 'hash', 'c', 'p', 'model-a', 'low', 'codex-app-server',
		'2026-01-01T00:00:00.000Z', 'succeeded')`)
	mustExec(`INSERT INTO analysis_run (id, article_id, job_id, attempt_count, content_hash, contract_version,
		prompt_version, requested_model, requested_effort, provider_id, started_at, status)
		VALUES ('run-failed', 'article-1', 'job-2', 1, 'hash', 'c', 'p', 'model-a', 'low', 'codex-app-server',
		'2026-01-01T00:00:00.000Z', 'failed')`)
	mustExec(`INSERT INTO analysis_run (id, article_id, job_id, attempt_count, content_hash, contract_version,
		prompt_version, requested_model, requested_effort, provider_id, started_at, status)
		VALUES ('run-running', 'article-1', 'job-3', 1, 'hash', 'c', 'p', 'model-a', 'low', 'codex-app-server',
		'2026-01-01T00:00:00.000Z', 'running')`)
	mustExec(`INSERT INTO analysis_stage_attempt (id, run_id, block_index, stage_id, status, provider_id, model_id,
		contract_version, prompt_version, started_at)
		VALUES ('attempt-1', 'run-succeeded', 0, 'linguistic_analysis', 'succeeded', 'codex-app-server', 'model-a',
		'c', 'p', '2026-01-01T00:00:00.000Z')`)
	mustExec(`INSERT INTO dictionary_entry (id, source_language, target_language, lookup_kind, lookup_form,
		normalized_lookup_form) VALUES ('entry-1', 'nl', 'en', 'word', 'huis', 'huis')`)

	// Apply 014 from the checked-in file, exactly as the production runner would.
	source := fstest.MapFS{"migrations/014_prompt_profiles_and_run_operations.sql": &fstest.MapFile{
		Data: checkedInMigration(t, "014_prompt_profiles_and_run_operations.sql"),
	}}
	if err := migrateWithSource(db, source); err != nil {
		t.Fatalf("apply 014: %v", err)
	}

	// The Explore binding is the translation row copied once, byte for field.
	var providerID, modelID, optionsJSON, optionsHash string
	if err := db.QueryRow(ctx, `SELECT provider_id, model_id, options_json, options_hash
		FROM analysis_profile_explore_binding WHERE profile_id = 'profile-1'`).Scan(
		&providerID, &modelID, &optionsJSON, &optionsHash); err != nil {
		t.Fatalf("read explore binding: %v", err)
	}
	if providerID != "codex-app-server" || modelID != "model-a" || optionsJSON != `{"reasoning_effort":"medium"}` || optionsHash != "hash-translation" {
		t.Fatalf("explore binding copy = %q %q %q %q", providerID, modelID, optionsJSON, optionsHash)
	}

	// Legacy runs keep article_analysis, empty subjects, and the phase
	// backfill only marks terminal rows finished.
	var operationType, subjectID, subjectLabel, phase string
	if err := db.QueryRow(ctx, `SELECT operation_type, subject_id, subject_label, phase FROM analysis_run WHERE id = 'run-succeeded'`).Scan(
		&operationType, &subjectID, &subjectLabel, &phase); err != nil {
		t.Fatal(err)
	}
	if operationType != "article_analysis" || subjectID != "" || subjectLabel != "" || phase != "finished" {
		t.Fatalf("terminal legacy run = %q %q %q %q", operationType, subjectID, subjectLabel, phase)
	}
	if err := db.QueryRow(ctx, `SELECT phase FROM analysis_run WHERE id = 'run-running'`).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "" {
		t.Fatalf("in-flight legacy run phase = %q, want empty until recovery finalizes it", phase)
	}
	var errorPhase string
	if err := db.QueryRow(ctx, `SELECT error_phase FROM analysis_stage_attempt WHERE id = 'attempt-1'`).Scan(&errorPhase); err != nil {
		t.Fatal(err)
	}
	if errorPhase != "" {
		t.Fatalf("legacy attempt error_phase = %q, want empty", errorPhase)
	}

	// Prompt identity: (prompt_type, version) is unique, versions are typed,
	// and selections must reference same-type versions.
	mustExec(`INSERT INTO prompt_version (id, prompt_type, version, instruction_text, content_hash)
		VALUES ('v-explore-1', 'explore', 1, 'instruction', 'hash-a')`)
	if _, err := db.Exec(ctx, `INSERT INTO prompt_version (id, prompt_type, version, instruction_text, content_hash)
		VALUES ('v-explore-dup', 'explore', 1, 'other', 'hash-b')`); err == nil {
		t.Fatal("duplicate (prompt_type, version) accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO prompt_version (id, prompt_type, version, instruction_text, content_hash)
		VALUES ('v-explore-0', 'explore', 0, 'other', 'hash-b')`); err == nil {
		t.Fatal("non-positive version accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO prompt_version (id, prompt_type, version, instruction_text, content_hash)
		VALUES ('v-unknown-type', 'poetry', 1, 'other', 'hash-b')`); err == nil {
		t.Fatal("unknown prompt type accepted")
	}
	mustExec(`INSERT INTO profile_prompt_selection (profile_id, prompt_type, prompt_version_id)
		VALUES ('profile-1', 'explore', 'v-explore-1')`)
	if _, err := db.Exec(ctx, `INSERT INTO profile_prompt_selection (profile_id, prompt_type, prompt_version_id)
		VALUES ('profile-1', 'correction', 'v-explore-1')`); err == nil {
		t.Fatal("cross-type selection accepted by schema")
	}

	// The dictionary entry's new nullable run pointer accepts a run and is
	// set back to NULL when that run disappears (ON DELETE SET NULL).
	mustExec(`UPDATE dictionary_entry SET last_run_id = 'run-succeeded' WHERE id = 'entry-1'`)
	if _, err := db.Exec(ctx, `DELETE FROM analysis_run WHERE id = 'run-succeeded'`); err != nil {
		t.Fatal(err)
	}
	var lastRun sql.NullString
	if err := db.QueryRow(ctx, `SELECT last_run_id FROM dictionary_entry WHERE id = 'entry-1'`).Scan(&lastRun); err != nil {
		t.Fatal(err)
	}
	if lastRun.Valid {
		t.Fatalf("dictionary last_run_id = %q, want NULL after run deletion", lastRun.String)
	}

	// Integrity and foreign keys hold across the migrated database.
	var integrity string
	if err := db.QueryRow(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q (err=%v)", integrity, err)
	}
	fkRows, err := db.Query(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer fkRows.Close()
	if fkRows.Next() {
		t.Fatal("foreign_key_check reported violations")
	}
}

// TestMigration014_FreshInstallAndRepeatedStartup proves an empty
// installation applies 014 cleanly and that re-running the migration runner
// (the repeated-startup path) is a no-op that keeps all rows.
func TestMigration014_FreshInstallAndRepeatedStartup(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	db := &DB{conn: conn}
	t.Cleanup(func() { _ = db.Close() })

	applyMigrationsThrough(t, db, "014_prompt_profiles_and_run_operations.sql")
	if _, err := db.Exec(context.Background(), `INSERT INTO prompt_version (id, prompt_type, version, instruction_text, content_hash)
		VALUES ('v-1', 'explore', 1, 'instruction', 'hash')`); err != nil {
		t.Fatal(err)
	}
	// A second full startup re-applies nothing and keeps stored rows.
	applyMigrationsThrough(t, db, "014_prompt_profiles_and_run_operations.sql")
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM prompt_version`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("prompt_version rows after repeated startup = %d, want 1", count)
	}
}
