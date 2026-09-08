package prompts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"doublangu/internal/store"
)

// ErrUnknownPromptType reports a prompt type outside the five fixed types.
var ErrUnknownPromptType = errors.New("unknown prompt type")

// ErrUnknownVersion reports a prompt version id that is not stored.
var ErrUnknownVersion = errors.New("unknown prompt version")

// ErrTypeMismatch reports a version resolved under a different prompt type
// than the caller declared. Selections must always pin a same-type version.
var ErrTypeMismatch = errors.New("prompt version does not match the requested type")

// Store owns the immutable prompt_version rows: transactional version saves,
// reads, and the idempotent default seeding. It never updates or deletes a
// stored version.
type Store struct {
	db *store.DB
}

// NewStore returns the prompt version store for db.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// Save validates and inserts the next immutable version of promptType. The
// version allocation and the insert share one transaction, so concurrent
// saves of the same type can never allocate the same version (the
// UNIQUE(prompt_type, version) index is the final backstop).
func (s *Store) Save(ctx context.Context, promptType PromptType, rawInstruction, rawLabel string) (*Version, error) {
	if !promptType.Valid() {
		return nil, ErrUnknownPromptType
	}
	instruction := NormalizeInstruction(rawInstruction)
	if err := ValidateInstruction(instruction); err != nil {
		return nil, err
	}
	label, err := ValidateLabel(rawLabel)
	if err != nil {
		return nil, err
	}
	version := &Version{
		ID:              NewVersionID(),
		PromptType:      promptType,
		Label:           label,
		InstructionText: instruction,
		ContentHash:     ContentHashOf(instruction),
	}
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		next, err := nextVersionTx(ctx, tx, promptType)
		if err != nil {
			return err
		}
		version.Version = next
		return insertVersionTx(ctx, tx, version)
	})
	if err != nil {
		return nil, err
	}
	return version, nil
}

// List returns every stored version of promptType, newest first.
func (s *Store) List(ctx context.Context, promptType PromptType) ([]Version, error) {
	if !promptType.Valid() {
		return nil, ErrUnknownPromptType
	}
	rows, err := s.db.Query(ctx, `SELECT id, prompt_type, version, label, instruction_text, content_hash, created_at
		FROM prompt_version WHERE prompt_type = ? ORDER BY version DESC`, string(promptType))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make([]Version, 0)
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.ID, &version.PromptType, &version.Version, &version.Label,
			&version.InstructionText, &version.ContentHash, &version.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// Get returns one stored version by id.
func (s *Store) Get(ctx context.Context, versionID string) (*Version, error) {
	var version Version
	err := s.db.QueryRow(ctx, `SELECT id, prompt_type, version, label, instruction_text, content_hash, created_at
		FROM prompt_version WHERE id = ?`, versionID).Scan(&version.ID, &version.PromptType, &version.Version,
		&version.Label, &version.InstructionText, &version.ContentHash, &version.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownVersion
	}
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// Resolve returns the stored version only when it exists and its type
// matches promptType. This is the matching-type check every selection and
// snapshot capture must pass.
func (s *Store) Resolve(ctx context.Context, promptType PromptType, versionID string) (*Version, error) {
	version, err := s.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if version.PromptType != promptType {
		return nil, fmt.Errorf("%w: version %s is %s, not %s", ErrTypeMismatch, versionID, version.PromptType, promptType)
	}
	return version, nil
}

// EnsureSeed inserts version 1 of every fixed type from the builtin defaults
// when that type has no stored version yet, and gives every existing profile
// default selections for types it has none for. It is idempotent and safe to
// run on every startup: stored versions and existing selections are never
// rewritten. Seeding runs in one transaction so a crash cannot leave a type
// half-seeded.
func (s *Store) EnsureSeed(ctx context.Context) error {
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		for _, promptType := range Types {
			if err := seedTypeTx(ctx, tx, promptType); err != nil {
				return err
			}
		}
		if err := seedSelectionsForExistingProfilesTx(ctx, tx); err != nil {
			return err
		}
		return nil
	})
}

// SeedProfileSelectionsTx pins one fresh profile's five prompt selections to
// the seeded defaults (v1 of each type) inside the caller's transaction.
// Profile creation calls this so an empty installation seeds on profile
// creation with exactly the same defaults startup seeding stores.
func SeedProfileSelectionsTx(ctx context.Context, tx *sql.Tx, profileID string) error {
	defaults, err := DefaultSelectionsTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, promptType := range Types {
		if err := insertSelectionTx(ctx, tx, profileID, promptType, defaults[promptType]); err != nil {
			return err
		}
	}
	return nil
}

// DefaultSelectionsTx returns the five default selection ids (the seeded v1
// of each type) inside the caller's transaction. Profile creation uses this
// so an empty installation seeds on profile creation with exactly the same
// defaults startup seeding would have stored. Missing seeds are created.
func DefaultSelectionsTx(ctx context.Context, tx *sql.Tx) (map[PromptType]string, error) {
	selections := make(map[PromptType]string, len(Types))
	for _, promptType := range Types {
		if err := seedTypeTx(ctx, tx, promptType); err != nil {
			return nil, err
		}
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM prompt_version WHERE prompt_type = ? AND version = 1`,
			string(promptType)).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("prompts: resolve seeded v1 of %s: %w", promptType, err)
		}
		selections[promptType] = id
	}
	return selections, nil
}

func seedTypeTx(ctx context.Context, tx *sql.Tx, promptType PromptType) error {
	if !promptType.Valid() {
		return ErrUnknownPromptType
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompt_version WHERE prompt_type = ?`,
		string(promptType)).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	instruction := NormalizeInstruction(DefaultInstruction(promptType))
	if err := ValidateInstruction(instruction); err != nil {
		return fmt.Errorf("prompts: default %s: %w", promptType, err)
	}
	return insertVersionTx(ctx, tx, &Version{
		ID:              NewVersionID(),
		PromptType:      promptType,
		Version:         1,
		InstructionText: instruction,
		ContentHash:     ContentHashOf(instruction),
	})
}

// seedSelectionsForExistingProfilesTx gives every profile that has no
// selection for a type the seeded v1 of that type. Existing selections are
// never moved.
func seedSelectionsForExistingProfilesTx(ctx context.Context, tx *sql.Tx) error {
	defaults, err := DefaultSelectionsTx(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM analysis_pipeline_profile`)
	if err != nil {
		return err
	}
	defer rows.Close()
	profileIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		profileIDs = append(profileIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, profileID := range profileIDs {
		for _, promptType := range Types {
			var existing int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_prompt_selection
				WHERE profile_id = ? AND prompt_type = ?`, profileID, string(promptType)).Scan(&existing); err != nil {
				return err
			}
			if existing > 0 {
				continue
			}
			if err := insertSelectionTx(ctx, tx, profileID, promptType, defaults[promptType]); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertVersionTx(ctx context.Context, tx *sql.Tx, version *Version) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO prompt_version (id, prompt_type, version, label, instruction_text, content_hash)
		VALUES (?, ?, ?, ?, ?, ?)`, version.ID, string(version.PromptType), version.Version, version.Label,
		version.InstructionText, version.ContentHash)
	if err != nil {
		return fmt.Errorf("prompts: save version of %s: %w", version.PromptType, err)
	}
	return nil
}

func insertSelectionTx(ctx context.Context, tx *sql.Tx, profileID string, promptType PromptType, versionID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO profile_prompt_selection (profile_id, prompt_type, prompt_version_id)
		VALUES (?, ?, ?)`, profileID, string(promptType), versionID)
	if err != nil {
		return fmt.Errorf("prompts: seed selection of %s for profile %s: %w", promptType, profileID, err)
	}
	return nil
}

func nextVersionTx(ctx context.Context, tx *sql.Tx, promptType PromptType) (int, error) {
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM prompt_version WHERE prompt_type = ?`,
		string(promptType)).Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

// Selection is one profile's pinned prompt version with its resolved identity.
type Selection struct {
	PromptType PromptType `json:"prompt_type"`
	VersionID  string     `json:"prompt_version_id"`
}

// SelectionsByProfile returns the stored selection rows of one profile keyed
// by prompt type.
func (s *Store) SelectionsByProfile(ctx context.Context, profileID string) (map[PromptType]Selection, error) {
	rows, err := s.db.Query(ctx, `SELECT prompt_type, prompt_version_id FROM profile_prompt_selection
		WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selections := make(map[PromptType]Selection)
	for rows.Next() {
		var selection Selection
		if err := rows.Scan(&selection.PromptType, &selection.VersionID); err != nil {
			return nil, err
		}
		selections[selection.PromptType] = selection
	}
	return selections, rows.Err()
}
