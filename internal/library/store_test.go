package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"doublangu/internal/store"
	"modernc.org/sqlite"
)

type chain struct {
	library Library
	work    Work
	edition Edition
	chapter Chapter
	asset   SourceAsset
}

func openLibraryDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func inTx(t *testing.T, db *store.DB, fn func(*sql.Tx)) {
	t.Helper()
	if err := db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		fn(tx)
		return nil
	}); err != nil {
		t.Fatalf("transaction: %v", err)
	}
}

func newLibrary(t *testing.T, name string) Library {
	t.Helper()
	record, err := NewLibrary(name, "nl-NL", "en-US", "description")
	if err != nil {
		t.Fatalf("new library: %v", err)
	}
	return record
}

func newWork(t *testing.T, libraryID ULID, title string) Work {
	t.Helper()
	record, err := NewWork(libraryID, title, "author", "ebook", "")
	if err != nil {
		t.Fatalf("new work: %v", err)
	}
	return record
}

func newEdition(t *testing.T, workID ULID, name string) Edition {
	t.Helper()
	record, err := NewEdition(workID, name, "nl-NL", "epub")
	if err != nil {
		t.Fatalf("new edition: %v", err)
	}
	return record
}

func newChapter(t *testing.T, editionID ULID, title string, number int) Chapter {
	t.Helper()
	record, err := NewChapter(editionID, title, number, 0, 120, 120)
	if err != nil {
		t.Fatalf("new chapter: %v", err)
	}
	return record
}

func newAsset(t *testing.T, chapterID ULID, url string) SourceAsset {
	t.Helper()
	record, err := NewSourceAsset(chapterID, url, "audio/mpeg", 42, "hash", 0, 120, 120)
	if err != nil {
		t.Fatalf("new source asset: %v", err)
	}
	return record
}

func createChain(t *testing.T, tx *sql.Tx, s *Store, prefix string) chain {
	t.Helper()
	ctx := context.Background()
	result := chain{library: newLibrary(t, prefix+" library")}
	if err := s.CreateLibrary(ctx, tx, &result.library); err != nil {
		t.Fatalf("create library: %v", err)
	}
	result.work = newWork(t, result.library.ID, prefix+" work")
	if err := s.CreateWork(ctx, tx, &result.work); err != nil {
		t.Fatalf("create work: %v", err)
	}
	result.edition = newEdition(t, result.work.ID, prefix+" edition")
	if err := s.CreateEdition(ctx, tx, &result.edition); err != nil {
		t.Fatalf("create edition: %v", err)
	}
	result.chapter = newChapter(t, result.edition.ID, prefix+" chapter", 1)
	if err := s.CreateChapter(ctx, tx, &result.chapter); err != nil {
		t.Fatalf("create chapter: %v", err)
	}
	result.asset = newAsset(t, result.chapter.ID, "file:///"+prefix+".mp3")
	if err := s.CreateSourceAsset(ctx, tx, &result.asset); err != nil {
		t.Fatalf("create source asset: %v", err)
	}
	return result
}

func assertKind(t *testing.T, err error, want Kind) {
	t.Helper()
	var storeErr *Error
	if !errors.As(err, &storeErr) || storeErr.Kind != want {
		t.Fatalf("error kind = %v, want %v (error %v)", storeErr, want, err)
	}
}

func assertNotFound(t *testing.T, err error) { assertKind(t, err, KindNotFound) }

func assertValidation(t *testing.T, err error) { assertKind(t, err, KindValidation) }

func assertConflict(t *testing.T, err error) {
	t.Helper()
	assertKind(t, err, KindConflict)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 19 {
		t.Fatalf("conflict does not unwrap SQLite constraint: %v", err)
	}
}

func assertIDs(t *testing.T, got, want []ULID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

func orderedID(t *testing.T, suffix string) ULID {
	t.Helper()
	id, err := ParseULID("01ARZ3NDEKTSV4RRFFQ69G5FA" + suffix)
	if err != nil {
		t.Fatalf("parse ordered ULID: %v", err)
	}
	return id
}

func setCreatedAt(t *testing.T, tx *sql.Tx, table string, id ULID, value string) {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(), fmt.Sprintf("UPDATE %s SET created_at = ? WHERE id = ?", table), value, id.String()); err != nil {
		t.Fatalf("set %s timestamp: %v", table, err)
	}
}

func TestStore_CRUD(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	inTx(t, db, func(tx *sql.Tx) {
		ctx := context.Background()
		missing := orderedID(t, "Z")
		libraries, err := s.ListLibraries(ctx, tx)
		if err != nil || libraries == nil || len(libraries) != 0 {
			t.Fatalf("empty libraries = %#v, %v", libraries, err)
		}
		works, err := s.ListWorksByLibrary(ctx, tx, missing)
		if err != nil || works == nil || len(works) != 0 {
			t.Fatalf("empty works = %#v, %v", works, err)
		}
		editions, err := s.ListEditionsByWork(ctx, tx, missing)
		if err != nil || editions == nil || len(editions) != 0 {
			t.Fatalf("empty editions = %#v, %v", editions, err)
		}
		chapters, err := s.ListChaptersByEdition(ctx, tx, missing)
		if err != nil || chapters == nil || len(chapters) != 0 {
			t.Fatalf("empty chapters = %#v, %v", chapters, err)
		}
		assets, err := s.ListSourceAssetsByChapter(ctx, tx, missing)
		if err != nil || assets == nil || len(assets) != 0 {
			t.Fatalf("empty source assets = %#v, %v", assets, err)
		}

		first, second := createChain(t, tx, s, "first"), createChain(t, tx, s, "second")
		libraries, err = s.ListLibraries(ctx, tx)
		if err != nil || len(libraries) != 2 {
			t.Fatalf("libraries = %#v, %v", libraries, err)
		}
		works, err = s.ListWorksByLibrary(ctx, tx, first.library.ID)
		if err != nil || len(works) != 1 || works[0].ID != first.work.ID {
			t.Fatalf("filtered works = %#v, %v", works, err)
		}
		editions, err = s.ListEditionsByWork(ctx, tx, first.work.ID)
		if err != nil || len(editions) != 1 || editions[0].ID != first.edition.ID {
			t.Fatalf("filtered editions = %#v, %v", editions, err)
		}
		chapters, err = s.ListChaptersByEdition(ctx, tx, first.edition.ID)
		if err != nil || len(chapters) != 1 || chapters[0].ID != first.chapter.ID {
			t.Fatalf("filtered chapters = %#v, %v", chapters, err)
		}
		assets, err = s.ListSourceAssetsByChapter(ctx, tx, first.chapter.ID)
		if err != nil || len(assets) != 1 || assets[0].ID != first.asset.ID {
			t.Fatalf("filtered source assets = %#v, %v", assets, err)
		}

		first.library.Name, first.library.SourceLanguage = "changed library", "NL-nl"
		first.work.Title = "changed work"
		first.edition.Name, first.edition.Language = "changed edition", "PT-br"
		first.chapter.Title, first.chapter.StartMs, first.chapter.EndMs, first.chapter.DurationMs = "changed chapter", 1, 1<<33, 1<<33
		first.asset.URL, first.asset.StartMs, first.asset.EndMs, first.asset.DurationMs = "file:///changed.mp3", 1, 1<<33, 1<<33
		for _, change := range []struct {
			name string
			err  error
		}{
			{"library", s.UpdateLibrary(ctx, tx, &first.library)},
			{"work", s.UpdateWork(ctx, tx, &first.work)},
			{"edition", s.UpdateEdition(ctx, tx, &first.edition)},
			{"chapter", s.UpdateChapter(ctx, tx, &first.chapter)},
			{"source asset", s.UpdateSourceAsset(ctx, tx, &first.asset)},
		} {
			if change.err != nil {
				t.Fatalf("update %s: %v", change.name, change.err)
			}
		}
		gotLibrary, err := s.GetLibrary(ctx, tx, first.library.ID)
		if err != nil || gotLibrary.Name != "changed library" || gotLibrary.SourceLanguage != "nl-NL" || gotLibrary.CreatedAt == "" {
			t.Fatalf("updated library = %#v, %v", gotLibrary, err)
		}
		gotWork, err := s.GetWork(ctx, tx, first.work.ID)
		if err != nil || gotWork.Title != "changed work" {
			t.Fatalf("updated work = %#v, %v", gotWork, err)
		}
		gotEdition, err := s.GetEdition(ctx, tx, first.edition.ID)
		if err != nil || gotEdition.Name != "changed edition" || gotEdition.Language != "pt-BR" {
			t.Fatalf("updated edition = %#v, %v", gotEdition, err)
		}
		gotChapter, err := s.GetChapter(ctx, tx, first.chapter.ID)
		if err != nil || gotChapter.Title != "changed chapter" || gotChapter.EndMs != 1<<33 || gotChapter.DurationMs != 1<<33 {
			t.Fatalf("updated chapter = %#v, %v", gotChapter, err)
		}
		gotAsset, err := s.GetSourceAsset(ctx, tx, first.asset.ID)
		if err != nil || gotAsset.URL != "file:///changed.mp3" || gotAsset.EndMs != 1<<33 || gotAsset.DurationMs != 1<<33 {
			t.Fatalf("updated asset = %#v, %v", gotAsset, err)
		}

		for _, deletion := range []struct {
			name   string
			delete func() error
			get    func() error
		}{
			{"source asset", func() error { return s.DeleteSourceAsset(ctx, tx, first.asset.ID) }, func() error { _, err := s.GetSourceAsset(ctx, tx, first.asset.ID); return err }},
			{"chapter", func() error { return s.DeleteChapter(ctx, tx, first.chapter.ID) }, func() error { _, err := s.GetChapter(ctx, tx, first.chapter.ID); return err }},
			{"edition", func() error { return s.DeleteEdition(ctx, tx, first.edition.ID) }, func() error { _, err := s.GetEdition(ctx, tx, first.edition.ID); return err }},
			{"work", func() error { return s.DeleteWork(ctx, tx, first.work.ID) }, func() error { _, err := s.GetWork(ctx, tx, first.work.ID); return err }},
			{"library", func() error { return s.DeleteLibrary(ctx, tx, first.library.ID) }, func() error { _, err := s.GetLibrary(ctx, tx, first.library.ID); return err }},
		} {
			if err := deletion.delete(); err != nil {
				t.Fatalf("delete %s: %v", deletion.name, err)
			}
			assertNotFound(t, deletion.get())
		}
		if _, err := s.GetLibrary(ctx, tx, second.library.ID); err != nil {
			t.Fatalf("filtered chain was deleted: %v", err)
		}
	})
}

func TestStore_Order(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	inTx(t, db, func(tx *sql.Tx) {
		ctx := context.Background()
		suffixes := []string{"C", "A", "D", "B"}
		timestamps := map[string]string{
			"A": "2026-01-01T00:00:00.000Z", "B": "2026-01-01T00:00:00.000Z",
			"C": "2026-01-02T00:00:00.000Z", "D": "2026-01-02T00:00:00.000Z",
		}
		want := []ULID{orderedID(t, "A"), orderedID(t, "B"), orderedID(t, "C"), orderedID(t, "D")}
		libraries := make([]Library, 0, len(suffixes))
		for _, suffix := range suffixes {
			record := newLibrary(t, "library "+suffix)
			record.ID = orderedID(t, suffix)
			if err := s.CreateLibrary(ctx, tx, &record); err != nil {
				t.Fatal(err)
			}
			setCreatedAt(t, tx, "library", record.ID, timestamps[suffix])
			libraries = append(libraries, record)
		}
		works := make([]Work, 0, len(suffixes))
		for _, suffix := range suffixes {
			record := newWork(t, libraries[0].ID, "work "+suffix)
			record.ID = orderedID(t, suffix)
			if err := s.CreateWork(ctx, tx, &record); err != nil {
				t.Fatal(err)
			}
			setCreatedAt(t, tx, "work", record.ID, timestamps[suffix])
			works = append(works, record)
		}
		editions := make([]Edition, 0, len(suffixes))
		for _, suffix := range suffixes {
			record := newEdition(t, works[0].ID, "edition "+suffix)
			record.ID = orderedID(t, suffix)
			if err := s.CreateEdition(ctx, tx, &record); err != nil {
				t.Fatal(err)
			}
			setCreatedAt(t, tx, "edition", record.ID, timestamps[suffix])
			editions = append(editions, record)
		}
		chapters := make([]Chapter, 0, len(suffixes))
		for _, suffix := range suffixes {
			record := newChapter(t, editions[0].ID, "chapter "+suffix, map[string]int{"A": 100, "B": 90, "C": 1, "D": 2}[suffix])
			record.ID = orderedID(t, suffix)
			if err := s.CreateChapter(ctx, tx, &record); err != nil {
				t.Fatal(err)
			}
			setCreatedAt(t, tx, "chapter", record.ID, timestamps[suffix])
			chapters = append(chapters, record)
		}
		assets := make([]SourceAsset, 0, len(suffixes))
		for _, suffix := range suffixes {
			record := newAsset(t, chapters[0].ID, "file:///"+suffix)
			record.ID = orderedID(t, suffix)
			if err := s.CreateSourceAsset(ctx, tx, &record); err != nil {
				t.Fatal(err)
			}
			setCreatedAt(t, tx, "source_asset", record.ID, timestamps[suffix])
			assets = append(assets, record)
		}
		gotLibraries, err := s.ListLibraries(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, []ULID{gotLibraries[0].ID, gotLibraries[1].ID, gotLibraries[2].ID, gotLibraries[3].ID}, want)
		gotWorks, err := s.ListWorksByLibrary(ctx, tx, libraries[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, []ULID{gotWorks[0].ID, gotWorks[1].ID, gotWorks[2].ID, gotWorks[3].ID}, want)
		gotEditions, err := s.ListEditionsByWork(ctx, tx, works[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, []ULID{gotEditions[0].ID, gotEditions[1].ID, gotEditions[2].ID, gotEditions[3].ID}, want)
		gotChapters, err := s.ListChaptersByEdition(ctx, tx, editions[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, []ULID{gotChapters[0].ID, gotChapters[1].ID, gotChapters[2].ID, gotChapters[3].ID}, want)
		gotAssets, err := s.ListSourceAssetsByChapter(ctx, tx, chapters[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, []ULID{gotAssets[0].ID, gotAssets[1].ID, gotAssets[2].ID, gotAssets[3].ID}, want)
	})
}

func TestStore_Validation(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	inTx(t, db, func(tx *sql.Tx) {
		ctx, badID := context.Background(), ULID("not-a-ulid")
		base := createChain(t, tx, s, "base")
		validate := func(name string, err error) {
			t.Helper()
			if err == nil {
				t.Fatalf("%s accepted invalid record", name)
			}
			assertValidation(t, err)
		}

		lib := newLibrary(t, "invalid")
		lib.ID = badID
		validate("create library id", s.CreateLibrary(ctx, tx, &lib))
		lib = newLibrary(t, "invalid")
		lib.SourceLanguage = "123"
		validate("create library language", s.CreateLibrary(ctx, tx, &lib))
		work := newWork(t, base.library.ID, "invalid")
		work.ID = badID
		validate("create work id", s.CreateWork(ctx, tx, &work))
		work = newWork(t, base.library.ID, "invalid")
		work.LibraryID = badID
		validate("create work parent", s.CreateWork(ctx, tx, &work))
		edition := newEdition(t, base.work.ID, "invalid")
		edition.ID = badID
		validate("create edition id", s.CreateEdition(ctx, tx, &edition))
		edition = newEdition(t, base.work.ID, "invalid")
		edition.WorkID = badID
		validate("create edition parent", s.CreateEdition(ctx, tx, &edition))
		edition = newEdition(t, base.work.ID, "invalid")
		edition.Language = "123"
		validate("create edition language", s.CreateEdition(ctx, tx, &edition))
		chapter := newChapter(t, base.edition.ID, "invalid", 2)
		chapter.ID = badID
		validate("create chapter id", s.CreateChapter(ctx, tx, &chapter))
		chapter = newChapter(t, base.edition.ID, "invalid", 2)
		chapter.EditionID = badID
		validate("create chapter parent", s.CreateChapter(ctx, tx, &chapter))
		chapter = newChapter(t, base.edition.ID, "invalid", 2)
		chapter.EndMs = -1
		validate("create chapter timing", s.CreateChapter(ctx, tx, &chapter))
		asset := newAsset(t, base.chapter.ID, "file:///invalid")
		asset.ID = badID
		validate("create asset id", s.CreateSourceAsset(ctx, tx, &asset))
		asset = newAsset(t, base.chapter.ID, "file:///invalid")
		asset.ChapterID = badID
		validate("create asset parent", s.CreateSourceAsset(ctx, tx, &asset))
		asset = newAsset(t, base.chapter.ID, "file:///invalid")
		asset.EndMs = -1
		validate("create asset timing", s.CreateSourceAsset(ctx, tx, &asset))

		lib = base.library
		lib.ID = badID
		validate("update library id", s.UpdateLibrary(ctx, tx, &lib))
		lib = base.library
		lib.SourceLanguage = "123"
		validate("update library language", s.UpdateLibrary(ctx, tx, &lib))
		work = base.work
		work.ID = badID
		validate("update work id", s.UpdateWork(ctx, tx, &work))
		work = base.work
		work.LibraryID = badID
		validate("update work parent", s.UpdateWork(ctx, tx, &work))
		edition = base.edition
		edition.ID = badID
		validate("update edition id", s.UpdateEdition(ctx, tx, &edition))
		edition = base.edition
		edition.WorkID = badID
		validate("update edition parent", s.UpdateEdition(ctx, tx, &edition))
		edition = base.edition
		edition.Language = "123"
		validate("update edition language", s.UpdateEdition(ctx, tx, &edition))
		chapter = base.chapter
		chapter.ID = badID
		validate("update chapter id", s.UpdateChapter(ctx, tx, &chapter))
		chapter = base.chapter
		chapter.EditionID = badID
		validate("update chapter parent", s.UpdateChapter(ctx, tx, &chapter))
		chapter = base.chapter
		chapter.EndMs = -1
		validate("update chapter timing", s.UpdateChapter(ctx, tx, &chapter))
		asset = base.asset
		asset.ID = badID
		validate("update asset id", s.UpdateSourceAsset(ctx, tx, &asset))
		asset = base.asset
		asset.ChapterID = badID
		validate("update asset parent", s.UpdateSourceAsset(ctx, tx, &asset))
		asset = base.asset
		asset.EndMs = -1
		validate("update asset timing", s.UpdateSourceAsset(ctx, tx, &asset))

		gotLibrary, err := s.GetLibrary(ctx, tx, base.library.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotWork, err := s.GetWork(ctx, tx, base.work.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotEdition, err := s.GetEdition(ctx, tx, base.edition.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotChapter, err := s.GetChapter(ctx, tx, base.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotAsset, err := s.GetSourceAsset(ctx, tx, base.asset.ID)
		if err != nil {
			t.Fatal(err)
		}
		if gotLibrary.Name != base.library.Name || gotWork.LibraryID != base.library.ID || gotEdition.WorkID != base.work.ID || gotChapter.EditionID != base.edition.ID || gotAsset.ChapterID != base.chapter.ID {
			t.Fatalf("invalid updates changed stored chain: %#v %#v %#v %#v %#v", gotLibrary, gotWork, gotEdition, gotChapter, gotAsset)
		}
	})
}

func TestStore_NotFoundConflictErrors(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	inTx(t, db, func(tx *sql.Tx) {
		ctx, base := context.Background(), createChain(t, tx, s, "base")
		missingLibrary := newLibrary(t, "missing")
		missingWork := newWork(t, base.library.ID, "missing")
		missingEdition := newEdition(t, base.work.ID, "missing")
		missingChapter := newChapter(t, base.edition.ID, "missing", 2)
		missingAsset := newAsset(t, base.chapter.ID, "file:///missing")
		for _, miss := range []struct {
			name string
			get  func() error
			edit func() error
			del  func() error
		}{
			{"library", func() error { _, err := s.GetLibrary(ctx, tx, missingLibrary.ID); return err }, func() error { return s.UpdateLibrary(ctx, tx, &missingLibrary) }, func() error { return s.DeleteLibrary(ctx, tx, missingLibrary.ID) }},
			{"work", func() error { _, err := s.GetWork(ctx, tx, missingWork.ID); return err }, func() error { return s.UpdateWork(ctx, tx, &missingWork) }, func() error { return s.DeleteWork(ctx, tx, missingWork.ID) }},
			{"edition", func() error { _, err := s.GetEdition(ctx, tx, missingEdition.ID); return err }, func() error { return s.UpdateEdition(ctx, tx, &missingEdition) }, func() error { return s.DeleteEdition(ctx, tx, missingEdition.ID) }},
			{"chapter", func() error { _, err := s.GetChapter(ctx, tx, missingChapter.ID); return err }, func() error { return s.UpdateChapter(ctx, tx, &missingChapter) }, func() error { return s.DeleteChapter(ctx, tx, missingChapter.ID) }},
			{"source asset", func() error { _, err := s.GetSourceAsset(ctx, tx, missingAsset.ID); return err }, func() error { return s.UpdateSourceAsset(ctx, tx, &missingAsset) }, func() error { return s.DeleteSourceAsset(ctx, tx, missingAsset.ID) }},
		} {
			assertNotFound(t, miss.get())
			assertNotFound(t, miss.edit())
			assertNotFound(t, miss.del())
		}
		duplicate := base.library
		assertConflict(t, s.CreateLibrary(ctx, tx, &duplicate))
		orphan := newWork(t, newLibrary(t, "missing parent").ID, "orphan")
		assertConflict(t, s.CreateWork(ctx, tx, &orphan))
		checkID := newLibrary(t, "check failure").ID
		_, err := tx.ExecContext(ctx, `INSERT INTO chapter (id, edition_id, start_ms, end_ms, duration_ms) VALUES (?, ?, -1, 0, 0)`, checkID.String(), base.edition.ID.String())
		assertConflict(t, writeError("check chapter", err))
	})
}

func TestStore_TransactionRollbackAndCascade(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	forced := errors.New("force rollback")
	err := db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		createChain(t, tx, s, "rollback")
		return forced
	})
	if !errors.Is(err, forced) {
		t.Fatalf("rollback error = %v", err)
	}
	inTx(t, db, func(tx *sql.Tx) {
		libraries, err := s.ListLibraries(context.Background(), tx)
		if err != nil || len(libraries) != 0 {
			t.Fatalf("rollback left libraries %#v, %v", libraries, err)
		}
	})

	var committed chain
	inTx(t, db, func(tx *sql.Tx) { committed = createChain(t, tx, s, "cascade") })
	inTx(t, db, func(tx *sql.Tx) {
		if err := s.DeleteLibrary(context.Background(), tx, committed.library.ID); err != nil {
			t.Fatal(err)
		}
		for _, get := range []func() error{
			func() error { _, err := s.GetLibrary(context.Background(), tx, committed.library.ID); return err },
			func() error { _, err := s.GetWork(context.Background(), tx, committed.work.ID); return err },
			func() error { _, err := s.GetEdition(context.Background(), tx, committed.edition.ID); return err },
			func() error { _, err := s.GetChapter(context.Background(), tx, committed.chapter.ID); return err },
			func() error { _, err := s.GetSourceAsset(context.Background(), tx, committed.asset.ID); return err },
		} {
			assertNotFound(t, get())
		}
	})
}

func TestStore_ConcurrentTransactions(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	const workers = 8
	type result struct {
		index int
		err   error
	}
	ready, start, results := make(chan struct{}, workers), make(chan struct{}), make(chan result, workers)
	for index := range workers {
		go func(index int) {
			ready <- struct{}{}
			<-start
			err := db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
				record, err := NewLibrary(fmt.Sprintf("concurrent-%d", index), "nl-NL", "en-US", "description")
				if err != nil {
					return err
				}
				if err := s.CreateLibrary(context.Background(), tx, &record); err != nil {
					return err
				}
				if index%2 == 0 {
					return fmt.Errorf("rollback-%d", index)
				}
				return nil
			})
			results <- result{index, err}
		}(index)
	}
	for range workers {
		<-ready
	}
	close(start)
	for range workers {
		result := <-results
		if result.index%2 == 0 && result.err == nil {
			t.Fatalf("worker %d unexpectedly committed", result.index)
		}
		if result.index%2 == 1 && result.err != nil {
			t.Fatalf("worker %d failed: %v", result.index, result.err)
		}
	}
	inTx(t, db, func(tx *sql.Tx) {
		libraries, err := s.ListLibraries(context.Background(), tx)
		if err != nil || len(libraries) != workers/2 {
			t.Fatalf("committed libraries = %#v, %v", libraries, err)
		}
		seen := make(map[string]bool, len(libraries))
		for _, library := range libraries {
			seen[library.Name] = true
		}
		for index := 1; index < workers; index += 2 {
			if !seen[fmt.Sprintf("concurrent-%d", index)] {
				t.Fatalf("missing committed worker %d: %#v", index, seen)
			}
		}
	})
}

func TestStore_CancelledOperationRollsBack(t *testing.T) {
	db, s := openLibraryDB(t), &Store{}
	var committed Library
	inTx(t, db, func(tx *sql.Tx) {
		committed = newLibrary(t, "committed")
		if err := s.CreateLibrary(context.Background(), tx, &committed); err != nil {
			t.Fatal(err)
		}
	})
	cancelled, cancel := context.WithCancel(context.Background())
	err := db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		pending := newLibrary(t, "pending")
		if err := s.CreateLibrary(context.Background(), tx, &pending); err != nil {
			return err
		}
		cancel()
		cancelledRecord := newLibrary(t, "cancelled")
		err := s.CreateLibrary(cancelled, tx, &cancelledRecord)
		if err == nil || !errors.Is(err, context.Canceled) || isConflict(err) {
			t.Fatalf("cancelled store operation = %v", err)
		}
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("transaction cancellation = %v", err)
	}
	inTx(t, db, func(tx *sql.Tx) {
		libraries, err := s.ListLibraries(context.Background(), tx)
		if err != nil || len(libraries) != 1 || libraries[0].ID != committed.ID {
			t.Fatalf("cancellation state = %#v, %v", libraries, err)
		}
	})
}

func TestStoreErrorUnwrapAndStrings(t *testing.T) {
	inner := errors.New("inner")
	storeErr := &Error{Op: "get library", Kind: KindNotFound, Err: inner}
	if !errors.Is(storeErr, inner) || storeErr.Error() == "" {
		t.Fatalf("store error does not unwrap: %v", storeErr)
	}
	for kind, want := range map[Kind]string{KindNotFound: "not found", KindValidation: "validation", KindConflict: "conflict", Kind(99): "unknown"} {
		if got := kind.String(); got != want {
			t.Fatalf("kind %d = %q, want %q", kind, got, want)
		}
	}
}

func isConflict(err error) bool {
	var storeErr *Error
	return errors.As(err, &storeErr) && storeErr.Kind == KindConflict
}
