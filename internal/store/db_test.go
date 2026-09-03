package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

func assertMigration003Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	expected := []string{
		"blob", "blob_reference",
		"idx_blob_reference_source_asset", "idx_blob_reference_digest",
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

	type foreignKey struct {
		table    string
		from     string
		to       string
		onDelete string
	}
	want := map[string]foreignKey{
		"source_asset_id": {table: "source_asset", from: "source_asset_id", to: "id", onDelete: "CASCADE"},
		"blob_digest":     {table: "blob", from: "blob_digest", to: "digest", onDelete: "RESTRICT"},
	}
	rows, err := db.Query(ctx, `PRAGMA foreign_key_list(blob_reference)`)
	if err != nil {
		t.Fatalf("list blob_reference foreign keys: %v", err)
	}
	defer rows.Close()
	found := make(map[string]foreignKey)
	for rows.Next() {
		var id, sequence int
		var key foreignKey
		var onUpdate, match string
		if err := rows.Scan(&id, &sequence, &key.table, &key.from, &key.to, &onUpdate, &key.onDelete, &match); err != nil {
			t.Fatalf("scan blob_reference foreign key: %v", err)
		}
		found[key.from] = key
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate blob_reference foreign keys: %v", err)
	}
	for from, expected := range want {
		actual, ok := found[from]
		if !ok || actual != expected {
			t.Errorf("foreign key %s = %+v, want %+v", from, actual, expected)
		}
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

func assertMigration004Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{
		"article", "article_block", "article_annotation", "learning_state",
		"idx_article_created", "idx_article_block_article", "idx_article_annotation_block_start",
		"idx_article_annotation_learning", "idx_learning_state_source_key",
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find reader schema object %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("reader schema object %s count = %d, want 1", name, count)
		}
	}
}

func assertMigration005Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{
		"job", "job_dependency", "analysis_cache", "semantic_item", "semantic_sense",
		"semantic_learning_state", "article_sentence", "article_occurrence",
		"article_occurrence_span", "speech_unit", "speech_profile", "audio_render",
		"audio_blob_reference", "article_occurrence_audio", "article_sentence_audio",
		"speech_worker", "speech_worker_enrollment", "idx_job_claim",
		"idx_audio_blob_reference_digest", "idx_article_sentence_audio_article", "idx_speech_unit_identity",
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find audible-reader schema object %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("audible-reader schema object %s count = %d, want 1", name, count)
		}
	}
}

func assertMigration006Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{
		"analysis_settings", "analysis_run", "analysis_turn", "analysis_chunk_cache",
		"idx_analysis_run_article_started", "idx_analysis_turn_run_order",
		"idx_analysis_chunk_cache_identity", "idx_analysis_cache_prepared_identity",
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find reliability schema object %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("reliability schema object %s count = %d, want 1", name, count)
		}
	}
}

func assertMigration007Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{
		"article_construction_member", "reader_settings", "idx_construction_member_token",
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find progressive-reader schema object %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("progressive-reader schema object %s count = %d, want 1", name, count)
		}
	}
	for _, column := range []string{"analysis_job_id", "sentence_revision"} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('article') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("article column %s count = %d err=%v, want 1", column, count, err)
		}
	}
	for _, column := range []string{
		"analysis_job_id", "analysis_status", "analysis_error_code",
		"published_analysis_job_id", "published_analysis_run_id",
		"published_analysis_revision", "published_analysis_model",
		"published_analysis_effort", "published_at",
	} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('article_block') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("article_block column %s count = %d err=%v, want 1", column, count, err)
		}
	}
	// reader_settings must exist as a seeded singleton with hover enabled.
	var count int
	var pronounceOnHover int
	var updatedAt string
	if err := db.QueryRow(ctx, `SELECT COUNT(*), COALESCE(MAX(pronounce_on_hover), -1), COALESCE(MAX(updated_at), '') FROM reader_settings`).Scan(&count, &pronounceOnHover, &updatedAt); err != nil {
		t.Fatalf("read reader_settings seed: %v", err)
	}
	if count != 1 || pronounceOnHover != 1 || updatedAt == "" {
		t.Errorf("reader_settings seed = count %d hover %d updated %q", count, pronounceOnHover, updatedAt)
	}
}

// TestMigration007_ProgressiveReaderSchema verifies the new tables, columns,
// constraints, and the seeded owner preference on a clean database.
func TestMigration007_ProgressiveReaderSchema(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	assertMigration007Schema(t, db)

	// reader_settings is a strict singleton whose value is constrained.
	if _, err := db.Exec(ctx, `INSERT INTO reader_settings (id, pronounce_on_hover, updated_at) VALUES (2, 1, 'now')`); err == nil {
		t.Fatal("second reader_settings row unexpectedly accepted")
	}
	if _, err := db.Exec(ctx, `UPDATE reader_settings SET pronounce_on_hover = 7`); err == nil {
		t.Fatal("out-of-range pronounce_on_hover unexpectedly accepted")
	}

	// article_block analysis_status CHECK accepts the documented vocabulary.
	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES ('01J00000000000000000000000', 'Prog', 'nl', 'en', 'draft')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J00000000000000000000001', '01J00000000000000000000000', 0, 'paragraph', 'Een zin.')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE article_block SET analysis_status = 'nonsense'`); err == nil {
		t.Fatal("invalid analysis_status unexpectedly accepted")
	}

	// Construction membership rows require distinct ordered members that
	// cascade with their occurrences.
	insertOccurrence := func(id, blockID string, role string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO article_occurrence (id, article_block_id, kind, role, shadow_policy) VALUES (?, ?, 'word', ?, 'none')`, id, blockID, role); err != nil {
			t.Fatalf("insert occurrence %s: %v", id, err)
		}
	}
	insertOccurrence("01J00000000000000000000010", "01J00000000000000000000001", "token")
	insertOccurrence("01J00000000000000000000011", "01J00000000000000000000001", "token")
	insertOccurrence("01J00000000000000000000012", "01J00000000000000000000001", "discontinuous_construction")
	if _, err := db.Exec(ctx, `INSERT INTO article_construction_member (construction_occurrence_id, token_occurrence_id, member_index) VALUES ('01J00000000000000000000012', '01J00000000000000000000010', 0), ('01J00000000000000000000012', '01J00000000000000000000011', 1)`); err != nil {
		t.Fatalf("insert construction members: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_construction_member (construction_occurrence_id, token_occurrence_id, member_index) VALUES ('01J00000000000000000000012', '01J00000000000000000000011', 2)`); err == nil {
		t.Fatal("duplicate member token unexpectedly accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_construction_member (construction_occurrence_id, token_occurrence_id, member_index) VALUES ('01J00000000000000000000012', '01J00000000000000000000011', 1)`); err == nil {
		t.Fatal("duplicate member order unexpectedly accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_construction_member (construction_occurrence_id, token_occurrence_id, member_index) VALUES ('01J00000000000000000000012', '01J00000000000000000000012', 3)`); err == nil {
		t.Fatal("self-member construction row unexpectedly accepted")
	}
	if _, err := db.Exec(ctx, `DELETE FROM article_occurrence WHERE id = '01J00000000000000000000010'`); err != nil {
		t.Fatal(err)
	}
	var members int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_construction_member WHERE token_occurrence_id = '01J00000000000000000000010'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Errorf("member row survived token occurrence delete: %d", members)
	}
}

// TestMigration007_UpgradeBackfill verifies the deterministic provider-free
// backfill of the progressive-reader columns from a 006 fixture.
func TestMigration007_UpgradeBackfill(t *testing.T) {
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

	through006 := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql",
	} {
		through006["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through006); err != nil {
		t.Fatalf("apply migrations through 006: %v", err)
	}
	ctx := context.Background()

	insertArticle := func(id, status, revision, model, effort string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status, content_hash, analysis_status, analysis_revision, analysis_error_code, analysis_model, analysis_effort, narration_status) VALUES (?, 'T', 'nl', 'en', 'ready', 'hash', ?, ?, '', ?, ?, 'not_requested')`, id, status, revision, model, effort); err != nil {
			t.Fatalf("insert article %s: %v", id, err)
		}
	}
	insertBlock := func(blockID, articleID string, index int) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, ?, 'paragraph', 'De bank staat.')`, blockID, articleID, index); err != nil {
			t.Fatalf("insert block %s: %v", blockID, err)
		}
	}
	insertSentence := func(sentenceID, blockID string, index int) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES (?, ?, ?, 0, 15, 'De bank staat.', 'sentence-hash')`, sentenceID, blockID, index); err != nil {
			t.Fatalf("insert sentence %s: %v", sentenceID, err)
		}
	}
	insertOccurrence := func(occurrenceID, blockID string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO article_occurrence (id, article_block_id, kind, role, shadow_policy, shadow_text) VALUES (?, ?, 'word', 'token', 'token', 'the bank')`, occurrenceID, blockID); err != nil {
			t.Fatalf("insert occurrence %s: %v", occurrenceID, err)
		}
	}

	// Article A is ready and has accepted semantics on block 0 only.
	insertArticle("01J000000000000000000000A0", "ready", "reader.analysis.v2", "legacy-model", "low")
	insertBlock("01J000000000000000000000A1", "01J000000000000000000000A0", 0)
	insertBlock("01J000000000000000000000A2", "01J000000000000000000000A0", 1)
	insertSentence("01J000000000000000000000A3", "01J000000000000000000000A1", 0)
	insertOccurrence("01J000000000000000000000A4", "01J000000000000000000000A1")

	// Article B failed before any paragraph was accepted.
	insertArticle("01J000000000000000000000B0", "failed", "", "", "")
	insertBlock("01J000000000000000000000B1", "01J000000000000000000000B0", 0)
	insertBlock("01J000000000000000000000B2", "01J000000000000000000000B0", 1)
	insertBlock("01J000000000000000000000B3", "01J000000000000000000000B0", 2)

	// Article C has a sentence row but no accepted semantics yet.
	insertArticle("01J000000000000000000000C0", "needs_analysis", "", "", "")
	insertBlock("01J000000000000000000000C1", "01J000000000000000000000C0", 0)
	insertSentence("01J000000000000000000000C2", "01J000000000000000000000C1", 0)

	// Article D failed after a reanalysis, but every block still holds an
	// older accepted materialization; no unresolved block exists.
	insertArticle("01J000000000000000000000D0", "failed", "reader.analysis.v2", "legacy-model", "medium")
	insertBlock("01J000000000000000000000D1", "01J000000000000000000000D0", 0)
	insertOccurrence("01J000000000000000000000D2", "01J000000000000000000000D1")

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("upgrade through migration 007: %v", err)
	}
	assertMigration007Schema(t, db)

	row := func(query string, args ...any) map[string]any {
		t.Helper()
		result := map[string]any{}
		rows, err := db.Query(ctx, query, args...)
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		defer rows.Close()
		columns, _ := rows.Columns()
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if !rows.Next() {
			t.Fatalf("query %q returned no rows", query)
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		for index, column := range columns {
			if text, ok := values[index].(string); ok {
				result[column] = text
			} else if number, ok := values[index].(int64); ok {
				result[column] = int(number)
			}
		}
		return result
	}

	// Rule 1: accepted blocks under a ready article keep their materialization
	// published with article provenance copied.
	blockA0 := row(`SELECT analysis_status, published_analysis_job_id, published_analysis_run_id, published_analysis_revision, published_analysis_model, published_analysis_effort, analysis_job_id FROM article_block WHERE id = '01J000000000000000000000A1'`)
	if blockA0["analysis_status"] != "ready" || blockA0["published_analysis_revision"] != "reader.analysis.v2" ||
		blockA0["published_analysis_model"] != "legacy-model" || blockA0["published_analysis_effort"] != "low" ||
		blockA0["published_analysis_job_id"] != "" || blockA0["published_analysis_run_id"] != "" ||
		blockA0["analysis_job_id"] != "" {
		t.Errorf("ready block provenance = %#v", blockA0)
	}

	// Rule 2: unresolved blocks stay pending; under a failed article the first
	// unresolved block is failed and later blocks remain pending.
	blockA1 := row(`SELECT analysis_status FROM article_block WHERE id = '01J000000000000000000000A2'`)
	if blockA1["analysis_status"] != "pending" {
		t.Errorf("unresolved block under ready article = %#v, want pending", blockA1)
	}
	blockB0 := row(`SELECT analysis_status FROM article_block WHERE id = '01J000000000000000000000B1'`)
	blockB1 := row(`SELECT analysis_status FROM article_block WHERE id = '01J000000000000000000000B2'`)
	blockB2 := row(`SELECT analysis_status FROM article_block WHERE id = '01J000000000000000000000B3'`)
	if blockB0["analysis_status"] != "failed" || blockB1["analysis_status"] != "pending" || blockB2["analysis_status"] != "pending" {
		t.Errorf("failed-article block states = %#v %#v %#v", blockB0, blockB1, blockB2)
	}

	// Rule 3: existing sentence rows are marked legacy; articles without rows
	// stay blank for lazy deterministic creation.
	articleA := row(`SELECT sentence_revision FROM article WHERE id = '01J000000000000000000000A0'`)
	articleB := row(`SELECT sentence_revision FROM article WHERE id = '01J000000000000000000000B0'`)
	articleC := row(`SELECT sentence_revision FROM article WHERE id = '01J000000000000000000000C0'`)
	if articleA["sentence_revision"] != "legacy.analysis" || articleC["sentence_revision"] != "legacy.analysis" {
		t.Errorf("legacy sentence revisions = A %#v C %#v", articleA, articleC)
	}
	if articleB["sentence_revision"] != "" {
		t.Errorf("sentence-less article revision = %#v, want empty", articleB)
	}

	// No semantic or sentence row is lost by the backfill.
	var occurrences, sentences int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_occurrence`).Scan(&occurrences); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence`).Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if occurrences != 2 || sentences != 2 {
		t.Errorf("row preservation = occurrences %d sentences %d", occurrences, sentences)
	}

	// Failed articles whose blocks all still carry accepted semantics keep
	// every block readable without a failed marker: article D.
	blockD0 := row(`SELECT analysis_status FROM article_block WHERE id = '01J000000000000000000000D1'`)
	if blockD0["analysis_status"] != "ready" {
		t.Errorf("failed article with accepted semantics = %#v, want ready", blockD0)
	}

	// A second migration pass must be a no-op and leave every state intact.
	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("second migration pass failed: %v", err)
	}
	blockA0Again := row(`SELECT analysis_status FROM article_block WHERE id = '01J000000000000000000000A1'`)
	if blockA0Again["analysis_status"] != "ready" {
		t.Errorf("ready block changed after no-op migration pass = %#v", blockA0Again)
	}
}

func assertMigration008Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{
		"analysis_pipeline_profile", "analysis_pipeline_binding", "analysis_pipeline_settings",
		"analysis_stage_cache", "analysis_stage_attempt", "analysis_stage_turn",
		"idx_stage_cache_identity", "idx_stage_attempt_run", "idx_stage_turn_attempt",
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("find pipeline schema object %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("pipeline schema object %s count = %d, want 1", name, count)
		}
	}
	for _, column := range []string{
		"pipeline_version", "profile_id", "profile_name", "profile_snapshot_json",
		"profile_snapshot_hash", "failed_stage_id", "failed_provider_id",
	} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('analysis_run') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("analysis_run column %s count = %d err=%v", column, count, err)
		}
	}
	for _, column := range []string{
		"analysis_profile_id", "analysis_profile_name",
		"analysis_pipeline_snapshot_json", "analysis_pipeline_snapshot_hash",
	} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('article') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("article column %s count = %d err=%v", column, count, err)
		}
	}
	for _, column := range []string{
		"published_analysis_profile_id", "published_analysis_profile_name",
		"published_analysis_snapshot_hash",
	} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('article_block') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("article_block column %s count = %d err=%v", column, count, err)
		}
	}
	for _, column := range []string{"translation_provider_id", "translation_provider_model"} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('semantic_sense') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("semantic_sense column %s count = %d err=%v", column, count, err)
		}
	}
}

func assertMigration009Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	var sql string
	if err := db.QueryRow(ctx, `SELECT sql FROM sqlite_master WHERE name = 'idx_stage_cache_identity'`).Scan(&sql); err != nil {
		t.Fatalf("read stage cache index: %v", err)
	}
	for _, column := range []string{"provider_type", "provider_config_fingerprint"} {
		if !strings.Contains(sql, column) {
			t.Errorf("stage cache identity index missing %s: %s", column, sql)
		}
	}
}

func assertMigration010Schema(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	for _, column := range []string{
		"usage_truncated", "timing_truncated", "metadata_truncated",
		"stderr_truncated", "error_detail_truncated",
	} {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pragma_table_info('analysis_stage_attempt') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Errorf("analysis_stage_attempt column %s count = %d err=%v", column, count, err)
		}
	}
}

// TestMigration008_ProfileCascadeAndLegacyPreservation proves profiles
// cascade through bindings/settings correctly and that a full 007->008
// upgrade preserves legacy analysis rows.
func TestMigration008_ProfileCascadeAndLegacyPreservation(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	assertMigration008Schema(t, db)
	assertMigration009Schema(t, db)
	assertMigration010Schema(t, db)

	if _, err := db.Exec(ctx, `INSERT INTO analysis_pipeline_profile (id, name) VALUES ('profile-1', 'Codex Only')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO analysis_pipeline_binding (profile_id, stage_id, provider_id, model_id, options_json, options_hash) VALUES ('profile-1', 'linguistic_analysis', 'codex-app-server', 'model-a', '{"reasoning_effort":"low"}', 'hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO analysis_pipeline_binding (profile_id, stage_id, provider_id, model_id, options_json, options_hash) VALUES ('profile-1', 'translation', 'codex-app-server', 'model-a', '{"reasoning_effort":"low"}', 'hash2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO analysis_pipeline_settings (id, active_profile_id, updated_at) VALUES (1, 'profile-1', 'now')`); err != nil {
		t.Fatal(err)
	}
	// Deleting the active profile is rejected by RESTRICT.
	if _, err := db.Exec(ctx, `DELETE FROM analysis_pipeline_profile WHERE id = 'profile-1'`); err == nil {
		t.Fatal("active profile deletion unexpectedly succeeded")
	}
	if _, err := db.Exec(ctx, `DELETE FROM analysis_pipeline_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM analysis_pipeline_profile WHERE id = 'profile-1'`); err != nil {
		t.Fatalf("profile deletion failed: %v", err)
	}
	var bindings int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_pipeline_binding WHERE profile_id = 'profile-1'`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Errorf("bindings survived profile delete: %d", bindings)
	}
}

// TestMigration008_UpgradeFrom007PreservesLegacyRows proves deterministic
// upgrade behavior: legacy settings/runs/turns/caches and accepted
// materializations survive, while old-format active jobs are canceled.
func TestMigration008_UpgradeFrom007PreservesLegacyRows(t *testing.T) {
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

	through007 := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql", "007_progressive_reader.sql",
	} {
		through007["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through007); err != nil {
		t.Fatalf("apply migrations through 007: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO analysis_settings (id, model, effort, updated_at) VALUES (1, 'model-a', 'low', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status, content_hash, analysis_status, analysis_revision, analysis_model, analysis_effort, narration_status, sentence_revision) VALUES ('01J00000000000000000000008', 'T', 'nl', 'en', 'ready', 'hash', 'ready', 'reader.analysis.v3', 'model-a', 'low', 'not_requested', 'legacy.analysis')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES ('job-legacy-1', 'reader.analysis.v2', 'server', 'article', '01J00000000000000000000008', 'key-1', 'hash', '{"contract_version":"reader.analysis.v3"}', 'queued')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO analysis_cache (id, content_hash, source_language, target_language, contract_version, provider_id, provider_model, provider_effort, prompt_version, prepared_input_hash, validated_response_json, response_hash) VALUES ('cache-1', 'hash', 'nl', 'en', 'reader.analysis.v2', 'provider', '', '', 'reader-analysis-prompt.v5', '', '{}', 'h')`); err != nil {
		t.Fatal(err)
	}

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("upgrade through migration 008: %v", err)
	}
	assertMigration008Schema(t, db)
	assertMigration009Schema(t, db)
	assertMigration010Schema(t, db)
	var model, effort string
	if err := db.QueryRow(ctx, `SELECT model, effort FROM analysis_settings WHERE id = 1`).Scan(&model, &effort); err != nil || model != "model-a" || effort != "low" {
		t.Fatalf("legacy settings lost: %q/%q err=%v", model, effort, err)
	}
	var cacheRows int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_cache`).Scan(&cacheRows); err != nil || cacheRows != 1 {
		t.Fatalf("legacy cache lost: %d err=%v", cacheRows, err)
	}
	var canceled int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE id = 'job-legacy-1' AND state = 'canceled' AND error_code = 'v1.analysis_pipeline_upgraded'`).Scan(&canceled); err != nil || canceled != 1 {
		t.Fatalf("legacy job not canceled: %d err=%v", canceled, err)
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT analysis_status FROM article WHERE id = '01J00000000000000000000008'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Errorf("article status after upgrade = %q, want ready (accepted analysis remains readable)", status)
	}
}

func TestMigration004ReaderConstraintsAndCascades(t *testing.T) {
	db, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID := "01J00000000000000000000000"
	blockID := "01J00000000000000000000001"
	annotationID := "01J00000000000000000000002"

	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, 'Test', 'nl', 'en', 'draft')`, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, 0, 'paragraph', 'Een woord.')`, blockID, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_annotation (id, article_block_id, start_utf16, end_utf16, source_text, kind, learning_key, primary_translation, suggest_shadow) VALUES (?, ?, 4, 9, 'woord', 'word', 'woord', 'word', 1)`, annotationID, blockID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO learning_state (source_language, kind, learning_key, status, updated_at) VALUES ('nl', 'word', 'woord', 'learned', 'now')`); err != nil {
		t.Fatal(err)
	}

	for name, statement := range map[string]string{
		"article status":  `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES ('01J00000000000000000000003', 'Bad', 'nl', 'en', 'unknown')`,
		"block kind":      `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J00000000000000000000003', '01J00000000000000000000000', 1, 'heading', 'Bad')`,
		"annotation kind": `INSERT INTO article_annotation (id, article_block_id, start_utf16, end_utf16, source_text, kind, learning_key, primary_translation, suggest_shadow) VALUES ('01J00000000000000000000003', '01J00000000000000000000001', 0, 1, 'E', 'clause', 'e', 'e', 0)`,
		"learning status": `INSERT INTO learning_state (source_language, kind, learning_key, status, updated_at) VALUES ('nl', 'word', 'e', 'unknown', 'now')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(ctx, statement); err == nil {
				t.Fatal("invalid reader row unexpectedly accepted")
			}
		})
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J00000000000000000000003', '01J00000000000000000000000', 0, 'paragraph', 'Duplicate index')`); err == nil {
		t.Fatal("duplicate article block index unexpectedly accepted")
	}

	if _, err := db.Exec(ctx, `DELETE FROM article WHERE id = ?`, articleID); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		table  string
		column string
		value  string
	}{
		{"article_block", "article_id", articleID},
		{"article_annotation", "id", annotationID},
	} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM "+item.table+" WHERE "+item.column+" = ?", item.value).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cascade left %s row count %d", item.table, count)
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
	expected := []string{"article", "article_annotation", "article_block", "blob", "blob_reference", "chapter", "device", "edition", "learning_state", "library", "outbox", "owner", "plugin_settings", "schema_version", "session", "source_asset", "work", "job", "semantic_sense", "audio_render", "speech_worker"}
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
	if version != 10 {
		t.Errorf("expected version 10, got %d", version)
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
	if count != 10 {
		t.Errorf("expected 10 migration records, got %d", count)
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
	if count != 10 {
		t.Errorf("expected 10 migration records, got %d (migrations should not reapply)", count)
	}
}

func TestMigrationRollbackLeavesNoPartialSchemaDataOrVersion(t *testing.T) {
	db := openPopulatedV1DB(t)
	v1ThroughV5 := fstest.MapFS{}
	for _, name := range []string{"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql", "005_audible_reader.sql"} {
		v1ThroughV5["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, v1ThroughV5); err != nil {
		t.Fatalf("apply migrations through 005: %v", err)
	}
	failing := fstest.MapFS{
		"migrations/006_probe.sql": {Data: []byte(`
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
	if err := db.QueryRow(context.Background(), "SELECT COUNT(*) FROM schema_version WHERE version = 6").Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 || versionCount != 0 {
		t.Fatalf("rollback left table=%d version=%d", tableCount, versionCount)
	}

	corrected := fstest.MapFS{
		"migrations/006_probe.sql": {Data: []byte(`
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

func TestMigration006PreservesLegacyRowsAndSeparatesCacheIdentities(t *testing.T) {
	db := openPopulatedV1DB(t)
	v1ThroughV5 := fstest.MapFS{}
	for _, name := range []string{"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql", "005_audible_reader.sql"} {
		v1ThroughV5["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, v1ThroughV5); err != nil {
		t.Fatalf("apply migrations through 005: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO analysis_cache (
		id, content_hash, source_language, target_language, contract_version,
		provider_id, provider_model, prompt_version, validated_response_json, response_hash
	) VALUES ('01J00000000000000000000010', 'content', 'nl', 'en', 'reader.analysis.v2', 'codex.appserver', 'legacy-model', 'reader-analysis-prompt.v2', '{}', 'legacy-hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO analysis_cache (
		id, content_hash, source_language, target_language, contract_version,
		provider_id, provider_model, prompt_version, validated_response_json, response_hash
	) VALUES ('01J00000000000000000000015', 'content-2', 'nl', 'en', 'reader.analysis.v2', 'codex.appserver', 'legacy-model', 'reader-analysis-prompt.v2', '{}', 'legacy-hash-2')`); err != nil {
		t.Fatal(err)
	}

	legacyOnly := fstest.MapFS{
		"migrations/006_analysis_reliability.sql": {Data: checkedInMigration(t, "006_analysis_reliability.sql")},
	}
	if err := migrateWithSource(db, legacyOnly); err != nil {
		t.Fatalf("apply migration 006: %v", err)
	}
	assertMigration006Schema(t, db)

	var preparedHash, effort string
	if err := db.QueryRow(ctx, `SELECT prepared_input_hash, provider_effort FROM analysis_cache WHERE id = '01J00000000000000000000010'`).Scan(&preparedHash, &effort); err != nil {
		t.Fatal(err)
	}
	if preparedHash != "" || effort != "" {
		t.Fatalf("legacy cache identity = %q/%q, want an inspectable non-hit", preparedHash, effort)
	}

	for _, row := range []struct {
		id, prepared, model, effort string
	}{
		{"01J00000000000000000000011", "prepared-a", "model-a", "low"},
		{"01J00000000000000000000012", "prepared-b", "model-b", "high"},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO analysis_cache (
			id, content_hash, source_language, target_language, contract_version,
			provider_id, provider_model, provider_effort, prompt_version,
			prepared_input_hash, validated_response_json, response_hash
		) VALUES (?, 'content', 'nl', 'en', 'reader.analysis.v2', 'codex.appserver', ?, ?, 'reader-analysis-prompt.v2', ?, '{}', ?)`, row.id, row.model, row.effort, row.prepared, row.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, input, model, effort string
	}{
		{"01J00000000000000000000013", "chunk-a", "model-a", "low"},
		{"01J00000000000000000000014", "chunk-b", "model-a", "high"},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO analysis_chunk_cache (
			id, source_language, target_language, content_hash, block_index, block_hash,
			carry_hash, chunk_input_hash, contract_version, prompt_version, provider_id,
			provider_model, provider_effort, validated_response_json, response_hash, created_at
		) VALUES (?, 'nl', 'en', 'content', 0, 'block', 'carry', ?, 'reader.analysis.v2', 'reader-analysis-prompt.v2', 'codex.appserver', ?, ?, '{}', ?, 'now')`, row.id, row.input, row.model, row.effort, row.id); err != nil {
			t.Fatal(err)
		}
	}
	var wholeCount, chunkCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_cache`).Scan(&wholeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_chunk_cache`).Scan(&chunkCount); err != nil {
		t.Fatal(err)
	}
	if wholeCount != 4 || chunkCount != 2 {
		t.Fatalf("cache rows = whole %d, chunks %d", wholeCount, chunkCount)
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
	assertMigration003Schema(t, db)
	assertMigration004Schema(t, db)
	assertMigration005Schema(t, db)
	assertMigration006Schema(t, db)
	assertMigration007Schema(t, db)
	assertMigration008Schema(t, db)
	assertMigration009Schema(t, db)
	assertMigration010Schema(t, db)
	assertMigrationVersion(t, db, 10)
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
	assertMigration003Schema(t, db)
	assertMigration004Schema(t, db)
	assertMigration005Schema(t, db)
	assertMigration006Schema(t, db)
	assertMigration007Schema(t, db)
	assertMigration008Schema(t, db)
	assertMigration009Schema(t, db)
	assertMigration010Schema(t, db)
	assertMigrationVersion(t, db, 10)
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
	if journal != "wal" || foreignKeys != 1 || busyTimeout != 5000 || version != 10 {
		t.Fatalf("journal=%q foreign_keys=%d busy_timeout=%d version=%d", journal, foreignKeys, busyTimeout, version)
	}
}

// TestMigration007_CancelsIncompatibleActiveAnalysisJobs proves that an
// upgrade with in-flight or queued v2 analysis jobs cancels them and moves
// their articles to the recoverable failed state without invoking a provider.
func TestMigration007_CancelsIncompatibleActiveAnalysisJobs(t *testing.T) {
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

	through006 := fstest.MapFS{}
	for _, name := range []string{
		"001_initial.sql", "002_library.sql", "003_media.sql", "004_reader_mvp.sql",
		"005_audible_reader.sql", "006_analysis_reliability.sql",
	} {
		through006["migrations/"+name] = &fstest.MapFile{Data: checkedInMigration(t, name)}
	}
	if err := migrateWithSource(db, through006); err != nil {
		t.Fatalf("apply migrations through 006: %v", err)
	}
	ctx := context.Background()
	insertArticle := func(id, status string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status, content_hash, analysis_status, narration_status) VALUES (?, 'T', 'nl', 'en', 'ready', 'hash', ?, 'not_requested')`, id, status); err != nil {
			t.Fatalf("insert article %s: %v", id, err)
		}
	}
	insertJob := func(jobID, articleID, state string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO job (id, job_type, execution_target, owner_type, owner_id, idempotency_key, input_hash, payload_json, state) VALUES (?, 'reader.analysis.v2', 'server', 'article', ?, ?, 'hash', '{"contract_version":"reader.analysis.v2"}', ?)`, jobID, articleID, jobID, state); err != nil {
			t.Fatalf("insert job %s: %v", jobID, err)
		}
	}
	insertArticle("01J000000000000000000000E0", "queued")
	insertJob("01J000000000000000000000E1", "01J000000000000000000000E0", "queued")
	insertArticle("01J000000000000000000000E2", "processing")
	insertJob("01J000000000000000000000E3", "01J000000000000000000000E2", "leased")

	if err := migrateWithSource(db, migrationFS); err != nil {
		t.Fatalf("upgrade through migration 007: %v", err)
	}
	var canceled int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE state = 'canceled' AND error_code = 'v1.analysis_contract_upgraded'`).Scan(&canceled); err != nil {
		t.Fatal(err)
	}
	if canceled != 2 {
		t.Errorf("canceled v2 jobs = %d, want 2", canceled)
	}
	var failed int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article WHERE analysis_status = 'failed' AND analysis_error_code = 'v1.analysis_contract_upgraded'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 2 {
		t.Errorf("transitioned articles = %d, want 2", failed)
	}
	var active int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE job_type = 'reader.analysis.v2' AND state IN ('queued', 'leased', 'running')`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Errorf("active v2 jobs after migration = %d", active)
	}
}
