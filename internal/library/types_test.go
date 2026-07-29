package library

import (
	"strings"
	"testing"
)

const uuidShapedID = "550e8400-e29b-41d4-a716-446655440000"

type recordChain struct {
	library     Library
	work        Work
	edition     Edition
	chapter     Chapter
	sourceAsset SourceAsset
}

func mustRecordChain(t *testing.T) recordChain {
	t.Helper()

	library, err := NewLibrary("Dutch Vocabulary", "NL-nl", "EN-us", "Core Dutch vocabulary")
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	work, err := NewWork(library.ID, "De Aanslag", "Harry Mulisch", "ebook", "")
	if err != nil {
		t.Fatalf("NewWork: %v", err)
	}
	edition, err := NewEdition(work.ID, "First Edition", "NL-nl", "epub")
	if err != nil {
		t.Fatalf("NewEdition: %v", err)
	}
	chapter, err := NewChapter(edition.ID, "Chapter 1", 1, 0, 120000, 120000)
	if err != nil {
		t.Fatalf("NewChapter: %v", err)
	}
	sourceAsset, err := NewSourceAsset(chapter.ID, "file:///data/audio/chapter1.mp3", "audio/mpeg", 5242880, "digest", 0, 120000, 120000)
	if err != nil {
		t.Fatalf("NewSourceAsset: %v", err)
	}
	return recordChain{library, work, edition, chapter, sourceAsset}
}

func TestRecordConstructorsGenerateStrictValidRecords(t *testing.T) {
	records := mustRecordChain(t)

	for _, test := range []struct {
		name     string
		id       ULID
		validate func() error
	}{
		{"library", records.library.ID, records.library.Validate},
		{"work", records.work.ID, records.work.Validate},
		{"edition", records.edition.ID, records.edition.Validate},
		{"chapter", records.chapter.ID, records.chapter.Validate},
		{"source_asset", records.sourceAsset.ID, records.sourceAsset.Validate},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParseULID(test.id.String())
			if err != nil {
				t.Fatalf("constructor ID %q did not strict-parse: %v", test.id, err)
			}
			if parsed != test.id || parsed.IsZero() {
				t.Fatalf("constructor ID = %q, want a non-zero strict ULID", test.id)
			}
			if err := test.validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}

	if records.library.SourceLanguage != "nl-NL" || records.library.TargetLanguage != "en-US" || records.edition.Language != "nl-NL" {
		t.Fatalf("constructors did not canonicalize languages: library=%q/%q edition=%q", records.library.SourceLanguage, records.library.TargetLanguage, records.edition.Language)
	}
}

func TestRecordValidatorsRejectUUIDShapedOwnAndParentIDs(t *testing.T) {
	t.Run("own IDs", func(t *testing.T) {
		records := mustRecordChain(t)
		for _, test := range []struct {
			name     string
			field    string
			mutate   func()
			validate func() error
		}{
			{"library", "library.id", func() { records.library.ID = ULID(uuidShapedID) }, records.library.Validate},
			{"work", "work.id", func() { records.work.ID = ULID(uuidShapedID) }, records.work.Validate},
			{"edition", "edition.id", func() { records.edition.ID = ULID(uuidShapedID) }, records.edition.Validate},
			{"chapter", "chapter.id", func() { records.chapter.ID = ULID(uuidShapedID) }, records.chapter.Validate},
			{"source_asset", "source_asset.id", func() { records.sourceAsset.ID = ULID(uuidShapedID) }, records.sourceAsset.Validate},
		} {
			t.Run(test.name, func(t *testing.T) {
				test.mutate()
				assertFieldError(t, test.validate(), test.field)
			})
		}
	})

	t.Run("parent IDs", func(t *testing.T) {
		records := mustRecordChain(t)
		for _, test := range []struct {
			name     string
			field    string
			mutate   func()
			validate func() error
		}{
			{"work library", "work.library_id", func() { records.work.LibraryID = ULID(uuidShapedID) }, records.work.Validate},
			{"edition work", "edition.work_id", func() { records.edition.WorkID = ULID(uuidShapedID) }, records.edition.Validate},
			{"chapter edition", "chapter.edition_id", func() { records.chapter.EditionID = ULID(uuidShapedID) }, records.chapter.Validate},
			{"source asset chapter", "source_asset.chapter_id", func() { records.sourceAsset.ChapterID = ULID(uuidShapedID) }, records.sourceAsset.Validate},
		} {
			t.Run(test.name, func(t *testing.T) {
				test.mutate()
				assertFieldError(t, test.validate(), test.field)
			})
		}
	})
}

func TestChildConstructorsRejectInvalidParentIDs(t *testing.T) {
	for _, test := range []struct {
		name        string
		parentField string
		construct   func() error
	}{
		{"work", "work.library_id", func() error { _, err := NewWork(ULID(uuidShapedID), "title", "", "", ""); return err }},
		{"edition", "edition.work_id", func() error { _, err := NewEdition(ULID(uuidShapedID), "name", "nl", ""); return err }},
		{"chapter", "chapter.edition_id", func() error { _, err := NewChapter(ULID(uuidShapedID), "title", 1, 0, 0, 0); return err }},
		{"source asset", "source_asset.chapter_id", func() error {
			_, err := NewSourceAsset(ULID(uuidShapedID), "file:///asset", "", 0, "", 0, 0, 0)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertFieldError(t, test.construct(), test.parentField)
		})
	}
}

func TestRecordValidatorsCanonicalizeAndRejectLanguages(t *testing.T) {
	records := mustRecordChain(t)

	rawLibrary := Library{
		ID:             records.library.ID,
		Name:           "Raw literal",
		SourceLanguage: "PT-br",
		TargetLanguage: "EN-us",
	}
	if err := rawLibrary.Validate(); err != nil {
		t.Fatalf("raw Library.Validate: %v", err)
	}
	if rawLibrary.SourceLanguage != "pt-BR" || rawLibrary.TargetLanguage != "en-US" {
		t.Fatalf("raw Library.Validate canonicalized %q/%q", rawLibrary.SourceLanguage, rawLibrary.TargetLanguage)
	}

	for _, test := range []struct {
		name  string
		field string
		valid func() error
	}{
		{"malformed source", "library.source_language", func() error { _, err := NewLibrary("name", "123", "en", ""); return err }},
		{"whitespace target", "library.target_language", func() error { _, err := NewLibrary("name", "nl", " en", ""); return err }},
		{"undetermined edition", "edition.language", func() error { _, err := NewEdition(records.work.ID, "name", "und", ""); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertFieldError(t, test.valid(), test.field)
		})
	}
}

func TestRecordValidatorsEnforceMillisecondBoundaries(t *testing.T) {
	records := mustRecordChain(t)
	large := int64(1<<31 + 1)

	for _, test := range []struct {
		name      string
		construct func(startMs, endMs, durationMs int64) error
	}{
		{"chapter", func(startMs, endMs, durationMs int64) error {
			_, err := NewChapter(records.edition.ID, "title", 1, startMs, endMs, durationMs)
			return err
		}},
		{"source asset", func(startMs, endMs, durationMs int64) error {
			_, err := NewSourceAsset(records.chapter.ID, "file:///asset", "", 0, "", startMs, endMs, durationMs)
			return err
		}},
	} {
		t.Run(test.name+" zero length", func(t *testing.T) {
			if err := test.construct(0, 0, 0); err != nil {
				t.Fatalf("zero-length interval: %v", err)
			}
		})
		t.Run(test.name+" large", func(t *testing.T) {
			if err := test.construct(0, large, large); err != nil {
				t.Fatalf("greater-than-32-bit interval: %v", err)
			}
		})
		t.Run(test.name+" negative", func(t *testing.T) {
			for _, values := range [][3]int64{{-1, 0, 0}, {0, -1, 0}, {0, 0, -1}} {
				if err := test.construct(values[0], values[1], values[2]); err == nil {
					t.Fatalf("negative timing %v unexpectedly validated", values)
				}
			}
		})
		t.Run(test.name+" reversed", func(t *testing.T) {
			if err := test.construct(1, 0, 0); err == nil {
				t.Fatal("reversed interval unexpectedly validated")
			}
		})
	}
}

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected validation error for %s", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("validation error %q does not identify %s", err, field)
	}
}
