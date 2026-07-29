package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"doublangu/internal/store"
)

func testStore(t *testing.T) (*Store, *store.DB) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	s, err := New(t.TempDir())
	if err != nil {
		db.Close()
		t.Fatalf("new media store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	return s, db
}

func assetID(n int) string { return fmt.Sprintf("01J123456789ABCDEF%08d", n) }

func makeSourceAsset(t *testing.T, ctx context.Context, db *store.DB, id string) {
	t.Helper()
	err := db.WithTransaction(ctx, func(tx *sql.Tx) error {
		for _, query := range []string{
			`INSERT OR IGNORE INTO library (id, name, source_language, target_language) VALUES ('01J123456789ABCDEF00000001', 'Test', 'nl', 'en')`,
			`INSERT OR IGNORE INTO work (id, library_id, title) VALUES ('01J123456789ABCDEF00000002', '01J123456789ABCDEF00000001', 'Work')`,
			`INSERT OR IGNORE INTO edition (id, work_id, name, language) VALUES ('01J123456789ABCDEF00000003', '01J123456789ABCDEF00000002', 'Edition', 'nl')`,
			`INSERT OR IGNORE INTO chapter (id, edition_id, title, chapter_num, start_ms, end_ms, duration_ms) VALUES ('01J123456789ABCDEF00000004', '01J123456789ABCDEF00000003', 'Chapter', 1, 0, 1000, 1000)`,
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO source_asset (id, chapter_id, url, mime_type, size_bytes, sha256_hash, start_ms, end_ms, duration_ms) VALUES (?, '01J123456789ABCDEF00000004', ?, 'audio/mpeg', 0, '', 0, 1000, 1000)`,
			id, "file:///"+id+".mp3")
		return err
	})
	if err != nil {
		t.Fatalf("create source asset %s: %v", id, err)
	}
}

func publish(t *testing.T, s *Store, db *store.DB, asset string, data []byte) string {
	t.Helper()
	digest, err := s.Write(context.Background(), db, asset, "application/octet-stream", data)
	if err != nil {
		t.Fatalf("publish %s: %v", asset, err)
	}
	return digest
}

func removeReference(t *testing.T, s *Store, db *store.DB, asset string) string {
	t.Helper()
	var digest string
	err := db.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		var err error
		digest, err = s.RemoveReference(context.Background(), tx, asset)
		return err
	})
	if err != nil {
		t.Fatalf("remove reference %s: %v", asset, err)
	}
	return digest
}

func referenceCount(t *testing.T, db *store.DB, digest string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM blob_reference WHERE blob_digest = ?`, digest).Scan(&count); err != nil {
		t.Fatalf("count references: %v", err)
	}
	return count
}

func blobCount(t *testing.T, db *store.DB, digest string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM blob WHERE digest = ?`, digest).Scan(&count); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	return count
}

func tempEntries(t *testing.T, s *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.root, "temp"))
	if err != nil {
		t.Fatalf("read temp directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func requireBytes(t *testing.T, s *Store, digest string, want []byte) {
	t.Helper()
	got, _, err := s.Read(digest)
	if err != nil {
		t.Fatalf("read %s: %v", digest, err)
	}
	if string(got) != string(want) {
		t.Fatalf("blob %s = %q, want %q", digest, got, want)
	}
}

func TestWriteReadAndReferenceLifecycle(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	asset := assetID(5)
	makeSourceAsset(t, ctx, db, asset)
	data := []byte("immutable media round trip")
	digest := publish(t, s, db, asset, data)

	if digest != sha256Hex(data) || blobCount(t, db, digest) != 1 || referenceCount(t, db, digest) != 1 {
		t.Fatalf("unexpected stored metadata for %s", digest)
	}
	requireBytes(t, s, digest, data)

	err := db.WithTransaction(ctx, func(tx *sql.Tx) error {
		ref, err := s.GetReference(ctx, tx, asset)
		if err != nil {
			return err
		}
		if ref.SourceAssetID != asset || ref.BlobDigest != digest {
			return fmt.Errorf("reference = %+v", ref)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("get reference: %v", err)
	}
}

func TestConcurrentSameDigestWriteSerializesAndDifferentDigestDoesNot(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	firstAsset, secondAsset, otherAsset := assetID(10), assetID(11), assetID(12)
	makeSourceAsset(t, ctx, db, firstAsset)
	makeSourceAsset(t, ctx, db, secondAsset)
	makeSourceAsset(t, ctx, db, otherAsset)
	data, otherData := []byte("same bytes behind a deterministic barrier"), []byte("different digest bypasses barrier")
	digest := sha256Hex(data)

	firstLocked := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondAtBoundary := make(chan struct{})
	type prepareResult struct {
		write *PreparedWrite
		err   error
	}
	secondPrepared := make(chan prepareResult, 1)
	differentPrepared := make(chan prepareResult, 1)
	var matchingAttempts atomic.Int32
	var matchingLocks atomic.Int32
	s.beforeLock = func(got string) {
		if got != digest {
			return
		}
		switch matchingAttempts.Add(1) {
		case 2:
			close(secondAtBoundary)
		}
	}
	s.afterLock = func(got string) {
		if got != digest || matchingLocks.Add(1) != 1 {
			return
		}
		close(firstLocked)
		<-releaseFirst
	}

	firstPrepared := make(chan prepareResult, 1)
	go func() {
		write, err := s.PrepareWrite(data)
		firstPrepared <- prepareResult{write: write, err: err}
	}()
	<-firstLocked
	go func() {
		write, err := s.PrepareWrite(data)
		secondPrepared <- prepareResult{write: write, err: err}
	}()
	<-secondAtBoundary
	select {
	case result := <-secondPrepared:
		if result.err != nil {
			t.Fatalf("second prepare: %v", result.err)
		}
		t.Fatal("second same-digest prepare crossed a held digest lease")
	default:
	}
	go func() {
		write, err := s.PrepareWrite(otherData)
		differentPrepared <- prepareResult{write: write, err: err}
	}()
	otherResult := <-differentPrepared
	if otherResult.err != nil {
		t.Fatalf("different prepare: %v", otherResult.err)
	}
	other := otherResult.write
	if err := s.AbandonWrite(other); err != nil {
		t.Fatalf("abandon different digest: %v", err)
	}

	close(releaseFirst)
	firstResult := <-firstPrepared
	if firstResult.err != nil {
		t.Fatalf("first prepare: %v", firstResult.err)
	}
	first := firstResult.write
	if _, err := s.StoreDB(ctx, db, firstAsset, "application/octet-stream", first); err != nil {
		t.Fatalf("commit first: %v", err)
	}
	secondResult := <-secondPrepared
	if secondResult.err != nil {
		t.Fatalf("second prepare: %v", secondResult.err)
	}
	second := secondResult.write
	if _, err := s.StoreDB(ctx, db, secondAsset, "application/octet-stream", second); err != nil {
		t.Fatalf("commit second: %v", err)
	}
	if matchingAttempts.Load() != 2 || matchingLocks.Load() != 2 {
		t.Fatalf("same digest attempts=%d locks=%d, want 2 each", matchingAttempts.Load(), matchingLocks.Load())
	}
	if blobCount(t, db, digest) != 1 || referenceCount(t, db, digest) != 2 || len(tempEntries(t, s)) != 0 {
		t.Fatalf("dedupe result is incomplete")
	}
	requireBytes(t, s, digest, data)
}

func TestRollbackWinnerLoserOrderingsPreserveBytesAndReferences(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, s *Store, db *store.DB, first, second string, data []byte)
	}{
		{
			name: "winner commits before loser abandons",
			run: func(t *testing.T, s *Store, db *store.DB, first, _ string, data []byte) {
				publish(t, s, db, first, data)
				loser, err := s.PrepareWrite(data)
				if err != nil {
					t.Fatalf("prepare loser: %v", err)
				}
				if err := s.AbandonWrite(loser); err != nil {
					t.Fatalf("abandon loser: %v", err)
				}
			},
		},
		{
			name: "loser abandons before winner commits",
			run: func(t *testing.T, s *Store, db *store.DB, first, _ string, data []byte) {
				loser, err := s.PrepareWrite(data)
				if err != nil {
					t.Fatalf("prepare loser: %v", err)
				}
				if err := s.AbandonWrite(loser); err != nil {
					t.Fatalf("abandon loser: %v", err)
				}
				publish(t, s, db, first, data)
			},
		},
		{
			name: "failed retry after existing winner",
			run: func(t *testing.T, s *Store, db *store.DB, first, second string, data []byte) {
				publish(t, s, db, first, data)
				failed, err := s.PrepareWrite(data)
				if err != nil {
					t.Fatalf("prepare failed retry: %v", err)
				}
				if _, err := s.StoreDB(context.Background(), db, "missing-source-asset", "application/octet-stream", failed); err == nil {
					t.Fatal("store with a missing source asset unexpectedly succeeded")
				}
				publish(t, s, db, second, data)
			},
		},
	}
	for n, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, db := testStore(t)
			ctx := context.Background()
			first, second := assetID(20+n*2), assetID(21+n*2)
			makeSourceAsset(t, ctx, db, first)
			makeSourceAsset(t, ctx, db, second)
			data := []byte("winner bytes must outlive every loser ordering")
			digest := sha256Hex(data)
			test.run(t, s, db, first, second, data)
			if blobCount(t, db, digest) != 1 || len(tempEntries(t, s)) != 0 {
				t.Fatalf("winner/loser cleanup left metadata or temporary bytes")
			}
			if n == 2 && referenceCount(t, db, digest) != 2 {
				t.Fatalf("successful retry references = %d, want 2", referenceCount(t, db, digest))
			}
			requireBytes(t, s, digest, data)
		})
	}
}

func TestCommitFailuresRestoreParityAndAllowRetry(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	winnerAsset, promotionAsset, retryAsset, commitAsset, freshCommitAsset := assetID(40), assetID(41), assetID(42), assetID(43), assetID(44)
	for _, asset := range []string{winnerAsset, promotionAsset, retryAsset, commitAsset, freshCommitAsset} {
		makeSourceAsset(t, ctx, db, asset)
	}
	winnerData := []byte("pre-existing winner survives byte-for-byte")
	winnerDigest := publish(t, s, db, winnerAsset, winnerData)

	promotionData := []byte("promotion failure has no metadata without bytes")
	promotionDigest := sha256Hex(promotionData)
	promotion, err := s.PrepareWrite(promotionData)
	if err != nil {
		t.Fatalf("prepare promotion failure: %v", err)
	}
	s.rename = func(_, _ string) error { return errors.New("injected promotion failure") }
	if _, err := s.StoreDB(ctx, db, promotionAsset, "application/octet-stream", promotion); err == nil {
		t.Fatal("injected promotion failure unexpectedly succeeded")
	}
	s.rename = nil
	if blobCount(t, db, promotionDigest) != 0 || len(tempEntries(t, s)) != 0 {
		t.Fatal("promotion failure left metadata or temporary bytes")
	}
	requireBytes(t, s, winnerDigest, winnerData)
	publish(t, s, db, promotionAsset, promotionData)

	commitAttempt, err := s.PrepareWrite(winnerData)
	if err != nil {
		t.Fatalf("prepare commit failure: %v", err)
	}
	s.commit = func(*sql.Tx) error { return errors.New("injected transaction commit failure") }
	if _, err := s.StoreDB(ctx, db, commitAsset, "application/octet-stream", commitAttempt); err == nil {
		t.Fatal("injected commit failure unexpectedly succeeded")
	}
	s.commit = nil
	if referenceCount(t, db, winnerDigest) != 1 || len(tempEntries(t, s)) != 0 {
		t.Fatal("commit failure changed winner metadata or left a temp")
	}
	requireBytes(t, s, winnerDigest, winnerData)
	publish(t, s, db, commitAsset, winnerData)
	if referenceCount(t, db, winnerDigest) != 2 {
		t.Fatalf("immediate retry references = %d, want 2", referenceCount(t, db, winnerDigest))
	}

	freshData := []byte("freshly promoted bytes roll back after commit failure")
	freshDigest := sha256Hex(freshData)
	freshAttempt, err := s.PrepareWrite(freshData)
	if err != nil {
		t.Fatalf("prepare fresh commit failure: %v", err)
	}
	commitFailure := errors.New("injected fresh transaction commit failure")
	promotionObserved := false
	s.commit = func(*sql.Tx) error {
		got, err := os.ReadFile(s.BlobPath(freshDigest))
		if err != nil {
			return fmt.Errorf("observe promoted fresh blob: %w", err)
		}
		if string(got) != string(freshData) {
			return fmt.Errorf("promoted fresh blob = %q, want %q", got, freshData)
		}
		promotionObserved = true
		return commitFailure
	}
	if _, err := s.StoreDB(ctx, db, freshCommitAsset, "application/octet-stream", freshAttempt); !errors.Is(err, commitFailure) {
		t.Fatalf("fresh commit failure = %v, want injected error", err)
	}
	s.commit = nil
	if !promotionObserved {
		t.Fatal("fresh commit failure did not reach blob promotion")
	}
	if blobCount(t, db, freshDigest) != 0 || referenceCount(t, db, freshDigest) != 0 {
		t.Fatal("fresh commit failure retained blob metadata or reference")
	}
	if _, err := os.Stat(s.BlobPath(freshDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh commit failure retained final blob: %v", err)
	}
	if entries := tempEntries(t, s); len(entries) != 0 {
		t.Fatalf("fresh commit failure retained temp artifacts: %v", entries)
	}
	publish(t, s, db, freshCommitAsset, freshData)
	if blobCount(t, db, freshDigest) != 1 || referenceCount(t, db, freshDigest) != 1 {
		t.Fatal("fresh same-digest retry did not restore metadata parity")
	}
	requireBytes(t, s, freshDigest, freshData)
}

func TestCommitHidesProvisionalPublicationFromRead(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	asset := assetID(50)
	makeSourceAsset(t, ctx, db, asset)
	data := []byte("reader waits for transaction finalization")
	digest := sha256Hex(data)
	pending, err := s.PrepareWrite(data)
	if err != nil {
		t.Fatalf("prepare pending write: %v", err)
	}
	commitEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	s.commit = func(tx *sql.Tx) error {
		close(commitEntered)
		<-releaseCommit
		return tx.Commit()
	}
	committed := make(chan error, 1)
	go func() {
		_, err := s.StoreDB(ctx, db, asset, "application/octet-stream", pending)
		committed <- err
	}()
	<-commitEntered
	readDone := make(chan struct{})
	readResult := make(chan error, 1)
	go func() {
		got, _, err := s.Read(digest)
		if err == nil && string(got) != string(data) {
			err = fmt.Errorf("read = %q, want %q", got, data)
		}
		readResult <- err
		close(readDone)
	}()
	select {
	case <-readDone:
		t.Fatal("read observed provisionally promoted bytes before metadata commit")
	default:
	}
	close(releaseCommit)
	if err := <-committed; err != nil {
		t.Fatalf("commit pending write: %v", err)
	}
	if err := <-readResult; err != nil {
		t.Fatalf("read after commit: %v", err)
	}
}

func TestInterruptedRecoveryCleansTempsAndProvisionalBytes(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	winnerAsset := assetID(60)
	makeSourceAsset(t, ctx, db, winnerAsset)
	winnerData := []byte("committed winner survives recovery")
	winnerDigest := publish(t, s, db, winnerAsset, winnerData)

	staleData := []byte("interrupted after temp creation")
	staleDigest := sha256Hex(staleData)
	stalePath := filepath.Join(s.root, "temp", staleDigest+".write-interrupted.tmp")
	if err := os.WriteFile(stalePath, staleData, 0600); err != nil {
		t.Fatalf("create stale write temp: %v", err)
	}
	provisionalData := []byte("interrupted after provisional publication")
	provisionalDigest := sha256Hex(provisionalData)
	if err := os.WriteFile(s.BlobPath(provisionalDigest), provisionalData, 0600); err != nil {
		t.Fatalf("create provisional blob: %v", err)
	}
	if err := s.Recover(ctx, db); err != nil {
		t.Fatalf("recover interruptions: %v", err)
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp exists after recovery: %v", err)
	}
	if _, err := os.Stat(s.BlobPath(provisionalDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provisional blob exists after recovery: %v", err)
	}
	if blobCount(t, db, provisionalDigest) != 0 || referenceCount(t, db, winnerDigest) != 1 {
		t.Fatal("recovery changed metadata outside interrupted artifacts")
	}
	requireBytes(t, s, winnerDigest, winnerData)
}

func TestOrphanCleanupWaitsRestoresAndReusesDigest(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	firstAsset, secondAsset, retryAsset := assetID(70), assetID(71), assetID(72)
	for _, asset := range []string{firstAsset, secondAsset, retryAsset} {
		makeSourceAsset(t, ctx, db, asset)
	}
	data := []byte("orphan cleanup has the same digest lease as writes")
	digest := publish(t, s, db, firstAsset, data)
	removeReference(t, s, db, firstAsset)

	writeLocked := make(chan struct{})
	releaseWrite := make(chan struct{})
	cleanupAtBoundary := make(chan struct{})
	var attempts atomic.Int32
	var locks atomic.Int32
	s.beforeLock = func(got string) {
		if got == digest && attempts.Add(1) == 2 {
			close(cleanupAtBoundary)
		}
	}
	s.afterLock = func(got string) {
		if got == digest && locks.Add(1) == 1 {
			close(writeLocked)
			<-releaseWrite
		}
	}
	type prepareResult struct {
		write *PreparedWrite
		err   error
	}
	pendingWrite := make(chan prepareResult, 1)
	go func() {
		write, err := s.PrepareWrite(data)
		pendingWrite <- prepareResult{write: write, err: err}
	}()
	<-writeLocked
	cleanupResult := make(chan struct {
		removed bool
		err     error
	}, 1)
	go func() {
		removed, err := s.CleanupOrphan(ctx, db, digest)
		cleanupResult <- struct {
			removed bool
			err     error
		}{removed, err}
	}()
	<-cleanupAtBoundary
	select {
	case result := <-cleanupResult:
		t.Fatalf("cleanup crossed held write lease: %+v", result)
	default:
	}
	close(releaseWrite)
	pendingResult := <-pendingWrite
	if pendingResult.err != nil {
		t.Fatalf("prepare competing write: %v", pendingResult.err)
	}
	pending := pendingResult.write
	if _, err := s.StoreDB(ctx, db, secondAsset, "application/octet-stream", pending); err != nil {
		t.Fatalf("commit competing write: %v", err)
	}
	if result := <-cleanupResult; result.err != nil || result.removed {
		t.Fatalf("cleanup after competing reference = %+v, want not removed", result)
	}
	if referenceCount(t, db, digest) != 1 {
		t.Fatalf("competing write references = %d, want 1", referenceCount(t, db, digest))
	}

	removeReference(t, s, db, secondAsset)
	s.commit = func(*sql.Tx) error { return errors.New("injected cleanup commit failure") }
	if removed, err := s.CleanupOrphan(ctx, db, digest); err == nil || removed {
		t.Fatalf("cleanup commit failure = removed:%t err:%v", removed, err)
	}
	s.commit = nil
	if blobCount(t, db, digest) != 1 {
		t.Fatal("failed cleanup deleted the blob record")
	}
	requireBytes(t, s, digest, data)
	removed, err := s.CleanupOrphan(ctx, db, digest)
	if err != nil || !removed {
		t.Fatalf("successful cleanup = removed:%t err:%v", removed, err)
	}
	if blobCount(t, db, digest) != 0 {
		t.Fatal("successful cleanup retained blob metadata")
	}
	if _, _, err := s.Read(digest); err == nil {
		t.Fatal("successful cleanup retained blob bytes")
	}
	publish(t, s, db, retryAsset, data)
	requireBytes(t, s, digest, data)
}

func TestCleanupOrphanRenameFailuresRemoveOrReportPlaceholder(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	asset := assetID(75)
	makeSourceAsset(t, ctx, db, asset)
	data := []byte("orphan rename failures retain database and filesystem parity")
	digest := publish(t, s, db, asset, data)
	removeReference(t, s, db, asset)
	blobPath := s.BlobPath(digest)

	renameFailure := errors.New("injected orphan rename failure")
	var cleanupPath string
	s.rename = func(oldPath, newPath string) error {
		if oldPath == blobPath {
			cleanupPath = newPath
			return renameFailure
		}
		return os.Rename(oldPath, newPath)
	}
	if removed, err := s.CleanupOrphan(ctx, db, digest); removed || !errors.Is(err, renameFailure) {
		t.Fatalf("rename failure = removed:%t err:%v", removed, err)
	}
	s.rename = nil
	if cleanupPath == "" {
		t.Fatal("rename failure did not allocate a cleanup path")
	}
	if blobCount(t, db, digest) != 1 {
		t.Fatal("rename failure removed orphan metadata")
	}
	requireBytes(t, s, digest, data)
	if entries := tempEntries(t, s); len(entries) != 0 {
		t.Fatalf("rename failure retained silent cleanup placeholder: %v", entries)
	}
	reuse, err := s.PrepareWrite(data)
	if err != nil {
		t.Fatalf("reuse digest lock after rename failure: %v", err)
	}
	if err := s.AbandonWrite(reuse); err != nil {
		t.Fatalf("release reused digest lock: %v", err)
	}

	removeFailure := errors.New("injected cleanup placeholder removal failure")
	cleanupPath = ""
	s.rename = func(oldPath, newPath string) error {
		if oldPath == blobPath {
			cleanupPath = newPath
			return renameFailure
		}
		return os.Rename(oldPath, newPath)
	}
	s.remove = func(path string) error {
		if path == cleanupPath {
			return removeFailure
		}
		return os.Remove(path)
	}
	removed, err := s.CleanupOrphan(ctx, db, digest)
	if removed || !errors.Is(err, renameFailure) || !errors.Is(err, removeFailure) {
		t.Fatalf("rename-plus-remove failure = removed:%t err:%v", removed, err)
	}
	if cleanupPath == "" || !strings.Contains(err.Error(), cleanupPath) || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("combined cleanup error lacks path/operation context: %v", err)
	}
	s.rename = nil
	s.remove = nil
	reuse, err = s.PrepareWrite(data)
	if err != nil {
		t.Fatalf("reuse digest lock after combined cleanup failure: %v", err)
	}
	if err := s.AbandonWrite(reuse); err != nil {
		t.Fatalf("release digest lock after combined cleanup failure: %v", err)
	}
	if err := s.Recover(ctx, db); err != nil {
		t.Fatalf("recover reported cleanup placeholder: %v", err)
	}
	if entries := tempEntries(t, s); len(entries) != 0 {
		t.Fatalf("recovery retained cleanup artifacts: %v", entries)
	}
	if blobCount(t, db, digest) != 1 {
		t.Fatal("recovery removed orphan metadata after failed rename")
	}
	requireBytes(t, s, digest, data)
}

func TestCopiedPreparedWriteFinalizesOnlyOnce(t *testing.T) {
	for _, copyFirst := range []bool{false, true} {
		name := "original first"
		if copyFirst {
			name = "copy first"
		}
		t.Run(name, func(t *testing.T) {
			s, db := testStore(t)
			ctx := context.Background()
			firstAsset, secondAsset := assetID(76), assetID(77)
			makeSourceAsset(t, ctx, db, firstAsset)
			makeSourceAsset(t, ctx, db, secondAsset)
			data := []byte("copied prepared wrapper shares one finalization state " + name)
			digest := sha256Hex(data)
			original, err := s.PrepareWrite(data)
			if err != nil {
				t.Fatalf("prepare copied wrapper: %v", err)
			}
			copied := *original
			first, second := original, &copied
			if copyFirst {
				first, second = &copied, original
			}
			if _, err := s.StoreDB(ctx, db, firstAsset, "application/octet-stream", first); err != nil {
				t.Fatalf("finalize first wrapper: %v", err)
			}
			if _, err := s.StoreDB(ctx, db, secondAsset, "application/octet-stream", second); !errors.Is(err, ErrFinalizedPreparedWrite) {
				t.Fatalf("finalize copied wrapper = %v, want finalized error", err)
			}
			if blobCount(t, db, digest) != 1 || referenceCount(t, db, digest) != 1 {
				t.Fatal("copied wrapper created duplicate metadata")
			}
			requireBytes(t, s, digest, data)
			if entries := tempEntries(t, s); len(entries) != 0 {
				t.Fatalf("copied wrapper retained temp artifacts: %v", entries)
			}
			reuse, err := s.PrepareWrite(data)
			if err != nil {
				t.Fatalf("reuse digest lock after copied finalization: %v", err)
			}
			if err := s.AbandonWrite(reuse); err != nil {
				t.Fatalf("release reused digest lock: %v", err)
			}
			if entries := tempEntries(t, s); len(entries) != 0 {
				t.Fatalf("lock reuse retained temp artifacts: %v", entries)
			}
		})
	}
}

func TestCleanupErrorsAndLifecycleTokensAreSafe(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	asset, otherAsset := assetID(80), assetID(81)
	makeSourceAsset(t, ctx, db, asset)
	makeSourceAsset(t, ctx, db, otherAsset)
	data := []byte("cleanup errors are surfaced and attempts remain isolated")
	pending, err := s.PrepareWrite(data)
	if err != nil {
		t.Fatalf("prepare abandoned write: %v", err)
	}
	pendingPath := pending.state.tempPath
	s.remove = func(path string) error {
		if path == pendingPath {
			return errors.New("injected temp removal failure")
		}
		return os.Remove(path)
	}
	err = s.AbandonWrite(pending)
	if err == nil || !strings.Contains(err.Error(), pendingPath) || !strings.Contains(err.Error(), "removal") {
		t.Fatalf("abandon cleanup error = %v, want path and operation context", err)
	}
	if err := s.AbandonWrite(pending); !errors.Is(err, ErrFinalizedPreparedWrite) {
		t.Fatalf("double abandon = %v, want finalized error", err)
	}
	if _, err := s.StoreDB(ctx, db, asset, "application/octet-stream", pending); !errors.Is(err, ErrFinalizedPreparedWrite) {
		t.Fatalf("commit after abandon = %v, want finalized error", err)
	}
	s.remove = nil
	if err := s.Recover(ctx, db); err != nil {
		t.Fatalf("recover failed temp: %v", err)
	}
	forged := &PreparedWrite{state: &preparedWriteState{
		store:    s,
		digest:   strings.Repeat("0", 64),
		size:     999,
		tempPath: filepath.Join(s.root, "temp", "substituted"),
	}}
	if _, err := s.StoreDB(ctx, db, asset, "application/octet-stream", forged); !errors.Is(err, ErrInvalidPreparedWrite) {
		t.Fatalf("forged digest/size capability = %v, want invalid token", err)
	}
	if _, err := s.StoreDB(ctx, db, asset, "application/octet-stream", &PreparedWrite{}); !errors.Is(err, ErrInvalidPreparedWrite) {
		t.Fatalf("zero prepared write = %v, want invalid token", err)
	}

	foreign, err := s.PrepareWrite(data)
	if err != nil {
		t.Fatalf("prepare foreign token: %v", err)
	}
	other, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new other store: %v", err)
	}
	if err := other.AbandonWrite(foreign); !errors.Is(err, ErrInvalidPreparedWrite) {
		t.Fatalf("foreign abandon = %v, want invalid token", err)
	}
	if _, err := s.StoreDB(ctx, db, asset, "application/octet-stream", foreign); err != nil {
		t.Fatalf("owner still commits foreign-rejected attempt: %v", err)
	}
	if _, err := s.StoreDB(ctx, db, otherAsset, "application/octet-stream", foreign); !errors.Is(err, ErrFinalizedPreparedWrite) {
		t.Fatalf("double commit = %v, want finalized error", err)
	}
	if err := s.AbandonWrite(foreign); !errors.Is(err, ErrFinalizedPreparedWrite) {
		t.Fatalf("abandon after commit = %v, want finalized error", err)
	}
	if _, err := s.StoreDB(ctx, db, asset, "application/octet-stream", nil); !errors.Is(err, ErrInvalidPreparedWrite) {
		t.Fatalf("nil prepared write = %v, want invalid token", err)
	}
	if _, _, err := s.Read("not-a-digest"); err == nil {
		t.Fatal("invalid digest read unexpectedly succeeded")
	}
}

func TestReferenceRestrictAndCascadeBehavior(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	asset := assetID(90)
	makeSourceAsset(t, ctx, db, asset)
	digest := publish(t, s, db, asset, []byte("foreign keys remain behavioral"))
	err := db.WithTransaction(ctx, func(tx *sql.Tx) error { return s.DeleteBlobRecord(ctx, tx, digest) })
	if err == nil {
		t.Fatal("deleting a referenced blob unexpectedly succeeded")
	}
	err = db.WithTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM source_asset WHERE id = ?`, asset)
		return err
	})
	if err != nil {
		t.Fatalf("delete source asset: %v", err)
	}
	if referenceCount(t, db, digest) != 0 || blobCount(t, db, digest) != 1 {
		t.Fatal("source asset cascade changed wrong blob/reference rows")
	}
}
