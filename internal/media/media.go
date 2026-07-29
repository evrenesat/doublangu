// Package media provides an immutable content-addressed byte store with
// transactional metadata references.
package media

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"doublangu/internal/store"

	"github.com/oklog/ulid/v2"
	"modernc.org/sqlite"
)

var (
	// ErrInvalidPreparedWrite indicates that a prepared write belongs to another
	// store or was not created by PrepareWrite.
	ErrInvalidPreparedWrite = errors.New("media: invalid prepared write")
	// ErrFinalizedPreparedWrite indicates that an attempt was already committed
	// or abandoned. Finalization is deliberately one-shot.
	ErrFinalizedPreparedWrite = errors.New("media: prepared write already finalized")
)

// Store manages immutable content-addressed media blobs at root. Operations
// that can change or observe a digest hold its digest lease until both the
// filesystem and database sides have reached a consistent terminal state.
type Store struct {
	root string

	mu    sync.Mutex
	locks map[string]*refCountedMutex

	// Test seams are nil in production. They are kept on Store so tests inject
	// failures into the same code path used by callers.
	beforeLock func(string)
	afterLock  func(string)
	rename     func(string, string) error
	remove     func(string) error
	commit     func(*sql.Tx) error
}

type refCountedMutex struct {
	mu    sync.Mutex
	count int
}

type digestLease struct {
	store  *Store
	digest string
	lock   *refCountedMutex
	once   sync.Once
}

func (l *digestLease) release() {
	l.once.Do(func() {
		l.lock.mu.Unlock()
		l.store.mu.Lock()
		l.lock.count--
		if l.lock.count == 0 {
			delete(l.store.locks, l.digest)
		}
		l.store.mu.Unlock()
	})
}

type preparedState uint8

const (
	prepared preparedState = iota
	finalizing
	finalized
)

type preparedWriteState struct {
	store    *Store
	digest   string
	size     int64
	tempPath string
	lease    *digestLease

	mu     sync.Mutex
	status preparedState
}

// PreparedWrite is an opaque, one-shot capability. Its package-owned shared
// state binds the verified digest, size, temporary path, and exactly one digest
// lease. Copying this exported wrapper therefore cannot duplicate lifecycle
// ownership or substitute finalization values.
type PreparedWrite struct {
	state *preparedWriteState
}

// Digest returns the verified content address for this write.
func (p *PreparedWrite) Digest() string {
	if p == nil || p.state == nil {
		return ""
	}
	return p.state.digest
}

func (p *PreparedWrite) begin(store *Store) error {
	if p == nil || p.state == nil {
		return ErrInvalidPreparedWrite
	}
	state := p.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.store != store ||
		state.lease == nil ||
		state.lease.store != store ||
		state.lease.digest != state.digest ||
		state.lease.lock == nil {
		return ErrInvalidPreparedWrite
	}
	if state.status != prepared {
		return ErrFinalizedPreparedWrite
	}
	state.status = finalizing
	return nil
}

func (p *PreparedWrite) finish() {
	state := p.state
	state.mu.Lock()
	state.status = finalized
	state.mu.Unlock()
	state.lease.release()
}

// BlobReference links a source_asset to its content-addressed blob.
type BlobReference struct {
	ID            string
	SourceAssetID string
	BlobDigest    string
	CreatedAt     string
}

// New creates a Store rooted at root. Recovery requires a database and is
// intentionally explicit through Recover so startup can surface failures.
func New(root string) (*Store, error) {
	for _, dir := range []string{"temp", "blobs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, fmt.Errorf("media: create %s: %w", dir, err)
		}
	}
	return &Store{root: root, locks: make(map[string]*refCountedMutex)}, nil
}

// PrepareWrite writes, syncs, and verifies data before it can enter metadata.
// Its unique temp file and digest lease are released only by CommitWrite or
// AbandonWrite.
func (s *Store) PrepareWrite(data []byte) (*PreparedWrite, error) {
	digest := sha256Hex(data)
	lease := s.lockDigest(digest)

	f, err := os.CreateTemp(filepath.Join(s.root, "temp"), digest+".write-*.tmp")
	if err != nil {
		lease.release()
		return nil, fmt.Errorf("media: create temp for %s: %w", digest, err)
	}
	tempPath := f.Name()
	if _, err := f.Write(data); err != nil {
		return nil, s.failPrepare(lease, tempPath, fmt.Errorf("media: write temp %s: %w", tempPath, err), f)
	}
	if err := f.Sync(); err != nil {
		return nil, s.failPrepare(lease, tempPath, fmt.Errorf("media: sync temp %s: %w", tempPath, err), f)
	}
	if err := f.Close(); err != nil {
		return nil, s.failPrepare(lease, tempPath, fmt.Errorf("media: close temp %s: %w", tempPath, err), nil)
	}
	if err := verifyFile(tempPath, digest, int64(len(data))); err != nil {
		return nil, s.failPrepare(lease, tempPath, err, nil)
	}

	return &PreparedWrite{state: &preparedWriteState{
		store:    s,
		digest:   digest,
		size:     int64(len(data)),
		tempPath: tempPath,
		lease:    lease,
		status:   prepared,
	}}, nil
}

func (s *Store) failPrepare(lease *digestLease, tempPath string, primary error, openFile *os.File) error {
	var cleanup error
	if openFile != nil {
		cleanup = openFile.Close()
	}
	if err := s.removeFile(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanup = errors.Join(cleanup, err)
	}
	lease.release()
	return joinFailure(primary, cleanup)
}

// CommitWrite owns the database transaction and the final immutable
// publication. The blob is promoted while the transaction is still open, but
// Read takes the same digest lease and cannot observe it until metadata commit
// succeeds. A failed promotion or commit removes only this attempt's artifact.
func (s *Store) CommitWrite(ctx context.Context, db *store.DB, sourceAssetID, mimeType string, write *PreparedWrite) (digest string, returnErr error) {
	if db == nil {
		return "", fmt.Errorf("media: commit: nil database")
	}
	if err := write.begin(s); err != nil {
		return "", err
	}
	defer write.finish()

	tx, err := db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return "", s.cleanupAttempt(write, fmt.Errorf("media: begin transaction: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				returnErr = joinFailure(returnErr, fmt.Errorf("media: rollback transaction: %w", err))
			}
		}
	}()

	winner, err := s.verifiedWinner(write)
	if err != nil {
		return "", s.cleanupAttempt(write, err)
	}
	if err := s.storeDB(ctx, tx, sourceAssetID, mimeType, write); err != nil {
		return "", s.cleanupAttempt(write, err)
	}

	published := false
	if !winner {
		if err := s.renameFile(write.state.tempPath, s.blobPath(write.state.digest)); err != nil {
			return "", s.cleanupAttempt(write, fmt.Errorf("media: promote %s: %w", write.state.tempPath, err))
		}
		published = true
	}

	if err := s.commitTransaction(tx); err != nil {
		rollbackErr := tx.Rollback()
		committed = true // the transaction has reached a terminal state.
		parityErr := s.restoreAfterCommitFailure(ctx, db, sourceAssetID, write, published)
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			parityErr = joinFailure(parityErr, fmt.Errorf("media: rollback failed commit: %w", rollbackErr))
		}
		return "", joinFailure(fmt.Errorf("media: commit transaction: %w", err), parityErr)
	}
	committed = true

	if winner {
		if err := s.removeFile(write.state.tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("media: discard duplicate temp %s after commit: %w", write.state.tempPath, err)
		}
	}
	return write.state.digest, nil
}

// StoreDB is the package-owned database/publication operation retained as the
// named media entry point. It accepts only an opaque prepared write, so callers
// cannot provide a different digest, size, or temporary path.
func (s *Store) StoreDB(ctx context.Context, db *store.DB, sourceAssetID, mimeType string, write *PreparedWrite) (string, error) {
	return s.CommitWrite(ctx, db, sourceAssetID, mimeType, write)
}

// Write is the one-call convenience wrapper for immutable publication.
func (s *Store) Write(ctx context.Context, db *store.DB, sourceAssetID, mimeType string, data []byte) (string, error) {
	write, err := s.PrepareWrite(data)
	if err != nil {
		return "", err
	}
	return s.CommitWrite(ctx, db, sourceAssetID, mimeType, write)
}

// AbandonWrite removes this attempt's temporary bytes and releases its lease.
// It is safe to call with a foreign or already-finalized capability: it returns
// an error and never unlocks another operation's lease.
func (s *Store) AbandonWrite(write *PreparedWrite) error {
	if err := write.begin(s); err != nil {
		return err
	}
	defer write.finish()
	if err := s.removeFile(write.state.tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media: abandon remove temp %s: %w", write.state.tempPath, err)
	}
	return nil
}

func (s *Store) storeDB(ctx context.Context, tx *sql.Tx, sourceAssetID, mimeType string, write *PreparedWrite) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO blob (digest, size_bytes, mime_type) VALUES (?, ?, ?)`,
		write.state.digest, write.state.size, mimeType,
	); err != nil {
		return writeError("insert blob", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blob_reference (id, source_asset_id, blob_digest) VALUES (?, ?, ?)
		ON CONFLICT(source_asset_id) DO UPDATE SET blob_digest = excluded.blob_digest`,
		newULID(), sourceAssetID, write.state.digest,
	); err != nil {
		return writeError("upsert blob reference", err)
	}
	return nil
}

func (s *Store) verifiedWinner(write *PreparedWrite) (bool, error) {
	path := s.blobPath(write.state.digest)
	if _, err := os.Stat(path); err == nil {
		if err := verifyFile(path, write.state.digest, write.state.size); err != nil {
			return false, fmt.Errorf("media: verify existing blob %s: %w", path, err)
		}
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("media: stat existing blob %s: %w", path, err)
	}
	return false, nil
}

func (s *Store) cleanupAttempt(write *PreparedWrite, primary error) error {
	if err := s.removeFile(write.state.tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return joinFailure(primary, fmt.Errorf("media: remove temp %s: %w", write.state.tempPath, err))
	}
	return primary
}

func (s *Store) restoreAfterCommitFailure(ctx context.Context, db *store.DB, sourceAssetID string, write *PreparedWrite, published bool) error {
	if !published {
		return s.cleanupAttempt(write, nil)
	}

	var count int
	queryErr := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM blob_reference WHERE source_asset_id = ? AND blob_digest = ?`,
		sourceAssetID, write.state.digest,
	).Scan(&count)
	if queryErr != nil {
		return fmt.Errorf("media: verify failed commit parity: %w", queryErr)
	}
	if count > 0 {
		// The driver reported an error after a durable commit. Keeping the promoted
		// bytes preserves parity; return the original commit error to the caller.
		return nil
	}
	if err := s.removeFile(s.blobPath(write.state.digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media: remove uncommitted blob %s: %w", write.state.digest, err)
	}
	return nil
}

func (s *Store) commitTransaction(tx *sql.Tx) error {
	if s.commit != nil {
		return s.commit(tx)
	}
	return tx.Commit()
}

// Read returns immutable bytes for digest. It waits for an in-flight
// publication or recovery for the same digest, so callers never read a
// provisionally promoted file.
func (s *Store) Read(digest string) ([]byte, string, error) {
	if !validDigest(digest) {
		return nil, "", fmt.Errorf("media: read: invalid digest %q", digest)
	}
	lease := s.lockDigest(digest)
	defer lease.release()
	data, err := os.ReadFile(s.blobPath(digest))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("media: blob %s: %w", digest, err)
		}
		return nil, "", fmt.Errorf("media: read blob %s: %w", digest, err)
	}
	return data, "", nil
}

// BlobPath returns the absolute filesystem path for a blob digest. Callers
// must not modify this path; media operations retain synchronization ownership.
func (s *Store) BlobPath(digest string) string { return s.blobPath(digest) }

// RemoveReference deletes the reference for sourceAssetID within the supplied
// transaction. Call CleanupOrphan only after that transaction commits.
func (s *Store) RemoveReference(ctx context.Context, tx *sql.Tx, sourceAssetID string) (string, error) {
	var digest string
	if err := tx.QueryRowContext(ctx,
		`SELECT blob_digest FROM blob_reference WHERE source_asset_id = ?`, sourceAssetID,
	).Scan(&digest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("media: reference for %s: %w", sourceAssetID, err)
		}
		return "", fmt.Errorf("media: get reference %s: %w", sourceAssetID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blob_reference WHERE source_asset_id = ?`, sourceAssetID); err != nil {
		return "", fmt.Errorf("media: delete reference: %w", err)
	}
	return digest, nil
}

// GetReference returns the blob reference for sourceAssetID if one exists.
func (s *Store) GetReference(ctx context.Context, tx *sql.Tx, sourceAssetID string) (*BlobReference, error) {
	var ref BlobReference
	err := tx.QueryRowContext(ctx,
		`SELECT id, source_asset_id, blob_digest, created_at FROM blob_reference WHERE source_asset_id = ?`, sourceAssetID,
	).Scan(&ref.ID, &ref.SourceAssetID, &ref.BlobDigest, &ref.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("media: reference %s: %w", sourceAssetID, sql.ErrNoRows)
		}
		return nil, fmt.Errorf("media: get reference %s: %w", sourceAssetID, err)
	}
	return &ref, nil
}

// CleanupOrphan atomically removes an unreferenced blob record and its bytes.
// It holds the digest lease, renames bytes to a private recovery artifact, and
// deletes that artifact only after the metadata transaction commits. On failure
// it restores the blob before releasing the lease.
func (s *Store) CleanupOrphan(ctx context.Context, db *store.DB, digest string) (removed bool, returnErr error) {
	if db == nil {
		return false, fmt.Errorf("media: orphan cleanup: nil database")
	}
	if !validDigest(digest) {
		return false, fmt.Errorf("media: orphan cleanup: invalid digest %q", digest)
	}
	lease := s.lockDigest(digest)
	defer lease.release()

	tx, err := db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("media: orphan begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				returnErr = joinFailure(returnErr, fmt.Errorf("media: orphan rollback transaction: %w", err))
			}
		}
	}()

	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM blob_reference WHERE blob_digest = ?`, digest,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("media: count references for %s: %w", digest, err)
	}
	if count > 0 {
		return false, nil
	}

	blobPath := s.blobPath(digest)
	cleanupPath := ""
	if _, err := os.Stat(blobPath); err == nil {
		cleanupPath, err = s.newCleanupPath(digest)
		if err != nil {
			return false, err
		}
		if err := s.renameFile(blobPath, cleanupPath); err != nil {
			primary := fmt.Errorf("media: move orphan %s aside: %w", blobPath, err)
			if removeErr := s.removeFile(cleanupPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				primary = joinFailure(primary, fmt.Errorf(
					"media: remove orphan cleanup placeholder %s after failed move: %w",
					cleanupPath,
					removeErr,
				))
			}
			return false, primary
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("media: stat orphan %s: %w", blobPath, err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM blob WHERE digest = ?`, digest)
	if err != nil {
		return false, s.restoreCleanupPath(blobPath, cleanupPath, fmt.Errorf("media: delete blob %s: %w", digest, err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, s.restoreCleanupPath(blobPath, cleanupPath, fmt.Errorf("media: orphan rows affected: %w", err))
	}
	if rows == 0 {
		return false, s.restoreCleanupPath(blobPath, cleanupPath, nil)
	}
	if err := s.commitTransaction(tx); err != nil {
		rollbackErr := tx.Rollback()
		committed = true // the transaction has reached a terminal state.
		var count int
		queryErr := db.QueryRow(ctx, `SELECT COUNT(*) FROM blob WHERE digest = ?`, digest).Scan(&count)
		primary := fmt.Errorf("media: orphan commit: %w", err)
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			primary = joinFailure(primary, fmt.Errorf("media: orphan rollback: %w", rollbackErr))
		}
		if queryErr != nil {
			return false, joinFailure(primary, fmt.Errorf("media: verify orphan commit: %w", queryErr))
		}
		if count == 0 {
			if cleanupPath != "" {
				if removeErr := s.removeFile(cleanupPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return false, joinFailure(primary, fmt.Errorf("media: remove durably deleted orphan %s: %w", cleanupPath, removeErr))
				}
			}
			return false, primary
		}
		return false, s.restoreCleanupPath(blobPath, cleanupPath, primary)
	}
	committed = true
	if cleanupPath != "" {
		if err := s.removeFile(cleanupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("media: remove committed orphan %s: %w", cleanupPath, err)
		}
	}
	return true, nil
}

func (s *Store) newCleanupPath(digest string) (string, error) {
	f, err := os.CreateTemp(filepath.Join(s.root, "temp"), digest+".cleanup-*.tmp")
	if err != nil {
		return "", fmt.Errorf("media: create orphan cleanup path: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", joinFailure(
			fmt.Errorf("media: close orphan cleanup path: %w", err),
			s.removeFile(path),
		)
	}
	return path, nil
}

func (s *Store) restoreCleanupPath(blobPath, cleanupPath string, primary error) error {
	if cleanupPath == "" {
		return primary
	}
	if err := s.renameFile(cleanupPath, blobPath); err != nil {
		return joinFailure(primary, fmt.Errorf("media: restore orphan %s: %w", blobPath, err))
	}
	return primary
}

// Recover removes stale write artifacts and resolves interrupted cleanup
// publications. It acquires each digest lease before touching a candidate and
// reports every cleanup failure to the caller.
func (s *Store) Recover(ctx context.Context, db *store.DB) error {
	if db == nil {
		return fmt.Errorf("media: recover: nil database")
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "temp"))
	if err != nil {
		return fmt.Errorf("media: read temp directory: %w", err)
	}
	var problems []error
	for _, entry := range entries {
		digest, cleanup := tempArtifact(entry.Name())
		if digest == "" {
			continue
		}
		lease := s.lockDigest(digest)
		path := filepath.Join(s.root, "temp", entry.Name())
		if cleanup {
			problems = append(problems, s.recoverCleanupArtifact(ctx, db, digest, path))
		} else if err := s.removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Errorf("media: recover stale temp %s: %w", path, err))
		}
		lease.release()
	}

	blobs, err := os.ReadDir(filepath.Join(s.root, "blobs"))
	if err != nil {
		return joinFailure(errors.Join(problems...), fmt.Errorf("media: read blob directory: %w", err))
	}
	for _, entry := range blobs {
		if entry.IsDir() || !validDigest(entry.Name()) {
			continue
		}
		digest := entry.Name()
		lease := s.lockDigest(digest)
		var count int
		err := db.QueryRow(ctx, `SELECT COUNT(*) FROM blob WHERE digest = ?`, digest).Scan(&count)
		if err != nil {
			problems = append(problems, fmt.Errorf("media: recover query blob %s: %w", digest, err))
		} else if count == 0 {
			path := s.blobPath(digest)
			if err := s.removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Errorf("media: recover provisional blob %s: %w", path, err))
			}
		}
		lease.release()
	}
	return errors.Join(problems...)
}

func (s *Store) recoverCleanupArtifact(ctx context.Context, db *store.DB, digest, cleanupPath string) error {
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM blob WHERE digest = ?`, digest).Scan(&count); err != nil {
		return fmt.Errorf("media: recover cleanup query %s: %w", digest, err)
	}
	if count == 0 {
		if err := s.removeFile(cleanupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("media: recover committed cleanup %s: %w", cleanupPath, err)
		}
		return nil
	}
	if _, err := os.Stat(s.blobPath(digest)); err == nil {
		if err := s.removeFile(cleanupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("media: remove duplicate cleanup %s: %w", cleanupPath, err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media: stat recovery blob %s: %w", digest, err)
	}
	if err := s.renameFile(cleanupPath, s.blobPath(digest)); err != nil {
		return fmt.Errorf("media: recover orphan %s: %w", digest, err)
	}
	return nil
}

// ListOrphanDigests returns digests of blobs that have no references.
func (s *Store) ListOrphanDigests(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT b.digest FROM blob b WHERE NOT EXISTS (SELECT 1 FROM blob_reference r WHERE r.blob_digest = b.digest) ORDER BY b.digest`,
	)
	if err != nil {
		return nil, fmt.Errorf("media: list orphans: %w", err)
	}
	defer rows.Close()
	var digests []string
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("media: scan orphan: %w", err)
		}
		digests = append(digests, digest)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("media: list orphan rows: %w", err)
	}
	if digests == nil {
		digests = []string{}
	}
	return digests, nil
}

// DeleteBlobRecord removes a blob row and reports a reference constraint.
func (s *Store) DeleteBlobRecord(ctx context.Context, tx *sql.Tx, digest string) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM blob WHERE digest = ?`, digest)
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 {
			return fmt.Errorf("media: blob %s has references: %w", digest, err)
		}
		return fmt.Errorf("media: delete blob %s: %w", digest, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("media: delete blob rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("media: blob %s not found", digest)
	}
	return nil
}

func (s *Store) lockDigest(digest string) *digestLease {
	if s.beforeLock != nil {
		s.beforeLock(digest)
	}
	s.mu.Lock()
	lock := s.locks[digest]
	if lock == nil {
		lock = &refCountedMutex{}
		s.locks[digest] = lock
	}
	lock.count++
	s.mu.Unlock()
	lock.mu.Lock()
	if s.afterLock != nil {
		s.afterLock(digest)
	}
	return &digestLease{store: s, digest: digest, lock: lock}
}

func (s *Store) renameFile(oldPath, newPath string) error {
	if s.rename != nil {
		return s.rename(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func (s *Store) removeFile(path string) error {
	if s.remove != nil {
		return s.remove(path)
	}
	return os.Remove(path)
}

func (s *Store) blobPath(digest string) string { return filepath.Join(s.root, "blobs", digest) }

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func verifyFile(path, expectedDigest string, expectedSize int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("media: open for verify %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("media: stat for verify %s: %w", path, err)
	}
	if info.Size() != expectedSize {
		return fmt.Errorf("media: size mismatch: got %d, want %d", info.Size(), expectedSize)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("media: hash for verify %s: %w", path, err)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expectedDigest {
		return fmt.Errorf("media: digest mismatch: got %s, want %s", actual, expectedDigest)
	}
	return nil
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func tempArtifact(name string) (digest string, cleanup bool) {
	const digestLength = sha256.Size * 2
	if len(name) <= digestLength || !validDigest(name[:digestLength]) || name[digestLength] != '.' || !strings.HasSuffix(name, ".tmp") {
		return "", false
	}
	if strings.HasPrefix(name[digestLength+1:], "cleanup-") {
		return name[:digestLength], true
	}
	if strings.HasPrefix(name[digestLength+1:], "write-") {
		return name[:digestLength], false
	}
	return "", false
}

func newULID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

func writeError(op string, err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 {
		return fmt.Errorf("media %s: conflict: %w", op, err)
	}
	return fmt.Errorf("media %s: %w", op, err)
}

func joinFailure(primary, cleanup error) error {
	if primary == nil {
		return cleanup
	}
	if cleanup == nil {
		return primary
	}
	return fmt.Errorf("%w; cleanup: %w", primary, cleanup)
}
