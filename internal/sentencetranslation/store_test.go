package sentencetranslation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func fixtureArticle(t *testing.T, db *store.DB, id, title string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, ?, 'nl', 'en', 'ready')`, id, title)
}

func fixtureBlock(t *testing.T, db *store.DB, id, articleID, text string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, 0, 'paragraph', ?)`, id, articleID, text)
}

func sourceHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func fixtureSentence(t *testing.T, db *store.DB, id, blockID, text string) string {
	t.Helper()
	hash := sourceHash(text)
	mustExec(t, db, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES (?, ?, 0, 0, ?, ?, ?)`,
		id, blockID, len([]rune(text)), text, hash)
	return hash
}

func saveParams(sentenceID library.ULID, hash string) SaveParams {
	return SaveParams{
		SentenceID: sentenceID, SourceHash: hash, TargetLanguage: "en",
		TranslationText: "She sits on a bench.", ResultHash: "res-hash",
		ProvenanceJSON: `{"op":"test"}`,
	}
}

func TestGetMissingSentence(t *testing.T) {
	db := testDB(t)
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	hash := fixtureSentence(t, db, "01J00000000000000000000SEN1", "01J00000000000000000000BLK1", "Zij zit op een bank.")

	s := NewStore(db)
	if _, err := s.Get(context.Background(), library.ULID("01J00000000000000000000SEN1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing translation err = %v", err)
	}

	saved, err := s.Save(context.Background(), saveParams(library.ULID("01J00000000000000000000SEN1"), hash))
	if err != nil {
		t.Fatal(err)
	}
	if saved.SourceHash != hash || saved.TargetLanguage != "en" {
		t.Fatalf("saved identity = %+v", saved)
	}
	if saved.TranslationText == nil || *saved.TranslationText != "She sits on a bench." {
		t.Fatalf("saved translation = %+v", saved)
	}
	if saved.ResultHash != "res-hash" || saved.ProvenanceJSON != `{"op":"test"}` {
		t.Fatalf("saved metadata = %+v", saved)
	}
	if saved.LastJobID != nil || saved.LastRunID != nil {
		t.Fatalf("job/run pointers must stay nullable, got %+v", saved)
	}

	again, err := s.Get(context.Background(), library.ULID("01J00000000000000000000SEN1"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Ready() || again.TranslationText == nil || *again.TranslationText != "She sits on a bench." {
		t.Fatalf("reread = %+v", again)
	}
}

func TestSaveRejectsStaleAnchor(t *testing.T) {
	db := testDB(t)
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	hash := fixtureSentence(t, db, "01J00000000000000000000SEN1", "01J00000000000000000000BLK1", "Zij zit op een bank.")

	s := NewStore(db)
	if _, err := s.Save(context.Background(), saveParams(library.ULID("01J00000000000000000000SEN1"), hash)); err != nil {
		t.Fatal(err)
	}

	// The anchor is recreated with different source text: the old result must
	// not attach to the new anchor by sentence id alone.
	mustExec(t, db, `UPDATE article_sentence SET source_text = 'Zij staat op een bank.', source_hash = ? WHERE id = '01J00000000000000000000SEN1'`, sourceHash("Zij staat op een bank."))
	if _, err := s.Save(context.Background(), saveParams(library.ULID("01J00000000000000000000SEN1"), hash)); !errors.Is(err, ErrStaleAnchor) {
		t.Fatalf("changed anchor err = %v", err)
	}

	// A deleted sentence rejects the same way.
	mustExec(t, db, `DELETE FROM article_sentence WHERE id = '01J00000000000000000000SEN1'`)
	if _, err := s.Save(context.Background(), saveParams(library.ULID("01J00000000000000000000SEN1"), hash)); !errors.Is(err, ErrStaleAnchor) {
		t.Fatalf("deleted anchor err = %v", err)
	}

	// The earlier saved translation is gone with its anchor; nothing was
	// remapped elsewhere.
	if _, err := s.Get(context.Background(), library.ULID("01J00000000000000000000SEN1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-delete get err = %v", err)
	}
}

func TestSaveRejectsForeignTargetLanguage(t *testing.T) {
	db := testDB(t)
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	hash := fixtureSentence(t, db, "01J00000000000000000000SEN1", "01J00000000000000000000BLK1", "Zij zit op een bank.")

	s := NewStore(db)
	params := saveParams(library.ULID("01J00000000000000000000SEN1"), hash)
	params.TargetLanguage = "de"
	if _, err := s.Save(context.Background(), params); err == nil || !strings.Contains(err.Error(), "target language") {
		t.Fatalf("foreign target err = %v", err)
	}
	if _, err := s.Get(context.Background(), library.ULID("01J00000000000000000000SEN1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed save must store nothing, get err = %v", err)
	}
}

func TestSaveRequiresIdentityAndText(t *testing.T) {
	db := testDB(t)
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	hash := fixtureSentence(t, db, "01J00000000000000000000SEN1", "01J00000000000000000000BLK1", "Zij zit op een bank.")

	s := NewStore(db)
	ctx := context.Background()
	sentence := library.ULID("01J00000000000000000000SEN1")

	blank := saveParams(sentence, hash)
	blank.TranslationText = "   "
	if _, err := s.Save(ctx, blank); err == nil {
		t.Fatal("blank translation must fail")
	}
	noHash := saveParams(sentence, hash)
	noHash.SourceHash = ""
	if _, err := s.Save(ctx, noHash); err == nil {
		t.Fatal("missing source hash must fail")
	}
	if _, err := s.Get(ctx, sentence); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed saves must store nothing, get err = %v", err)
	}
}

func TestSaveReplacesOnRegeneration(t *testing.T) {
	db := testDB(t)
	fixtureArticle(t, db, "01J00000000000000000000ART1", "Fixture")
	fixtureBlock(t, db, "01J00000000000000000000BLK1", "01J00000000000000000000ART1", "De bank.")
	hash := fixtureSentence(t, db, "01J00000000000000000000SEN1", "01J00000000000000000000BLK1", "Zij zit op een bank.")

	s := NewStore(db)
	ctx := context.Background()
	sentence := library.ULID("01J00000000000000000000SEN1")
	if _, err := s.Save(ctx, saveParams(sentence, hash)); err != nil {
		t.Fatal(err)
	}
	replacement := saveParams(sentence, hash)
	replacement.TranslationText = "She is sitting on a bench."
	replacement.ResultHash = "res-hash-2"
	saved, err := s.Save(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if saved.TranslationText == nil || *saved.TranslationText != "She is sitting on a bench." || saved.ResultHash != "res-hash-2" {
		t.Fatalf("replacement = %+v", saved)
	}
}
