package analysis

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"doublangu/internal/config"
	"doublangu/internal/library"
	"doublangu/internal/pipeline"
	"doublangu/internal/store"
)

// ErrProfileNotFound distinguishes missing profiles from database failures.
var ErrProfileNotFound = errors.New("analysis profile not found")

// ErrProfileConflict reports duplicate names and active-profile deletion.
type ProfileConflictError struct{ Reason string }

func (e *ProfileConflictError) Error() string { return "analysis profile conflict: " + e.Reason }

// Profile is the stored mutable profile with its two stage bindings.
type Profile struct {
	ID        string                     `json:"id"`
	Name      string                     `json:"name"`
	Bindings  []pipeline.BindingSnapshot `json:"bindings"`
	IsActive  bool                       `json:"is_active"`
	UpdatedAt string                     `json:"updated_at"`
}

// ProfileStore owns transactional profile CRUD and activation. Binding
// canonicalization and catalog policy live in the service layer; the store
// enforces shape, stage identity, and uniqueness.
type ProfileStore struct {
	db *store.DB
}

func NewProfileStore(db *store.DB) *ProfileStore { return &ProfileStore{db: db} }

// CanonicalizeBindings validates stage identity, canonicalizes options through
// the provider type codec, and returns bindings in registered stage order.
// Callers supply the provider type map resolved from the running registry.
func CanonicalizeBindings(providerTypes map[string]string, raw []pipeline.BindingSnapshot) ([]pipeline.BindingSnapshot, error) {
	sorted, err := pipeline.SortBindings(raw)
	if err != nil {
		return nil, err
	}
	for index := range sorted {
		binding := &sorted[index]
		providerType, ok := providerTypes[binding.ProviderID]
		if !ok {
			return nil, fmt.Errorf("binding %s references unknown provider %q", binding.StageID, binding.ProviderID)
		}
		canonical, err := config.CanonicalizeProviderOptions(providerType, binding.Options)
		if err != nil {
			return nil, fmt.Errorf("binding %s options: %w", binding.StageID, err)
		}
		optionsHash, err := pipeline.OptionsHashOf(canonical)
		if err != nil {
			return nil, err
		}
		binding.ProviderType = providerType
		binding.Options = canonical
		binding.OptionsHash = optionsHash
		contract, prompt, ok := pipeline.StageContracts(binding.StageID)
		if !ok {
			return nil, fmt.Errorf("unregistered stage %q", binding.StageID)
		}
		binding.ContractVersion = contract
		binding.PromptVersion = prompt
	}
	return sorted, nil
}

// List returns every profile with active state, newest first.
func (s *ProfileStore) List(ctx context.Context) ([]Profile, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id, p.name, p.updated_at,
		EXISTS(SELECT 1 FROM analysis_pipeline_settings st WHERE st.active_profile_id = p.id)
		FROM analysis_pipeline_profile p ORDER BY p.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]Profile, 0)
	for rows.Next() {
		var profile Profile
		var active int
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.UpdatedAt, &active); err != nil {
			return nil, err
		}
		profile.IsActive = active == 1
		profile.Bindings = []pipeline.BindingSnapshot{}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	bindings, err := s.listBindings(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*Profile, len(profiles))
	for index := range profiles {
		byID[profiles[index].ID] = &profiles[index]
	}
	for _, binding := range bindings {
		if profile, ok := byID[binding.profileID]; ok {
			profile.Bindings = append(profile.Bindings, binding.snapshot)
		}
	}
	return profiles, nil
}

func (s *ProfileStore) listBindings(ctx context.Context) ([]struct {
	profileID string
	snapshot  pipeline.BindingSnapshot
}, error) {
	rows, err := s.db.Query(ctx, `SELECT profile_id, stage_id, provider_id, model_id, options_json, options_hash
		FROM analysis_pipeline_binding ORDER BY profile_id, stage_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]struct {
		profileID string
		snapshot  pipeline.BindingSnapshot
	}, 0)
	for rows.Next() {
		var item struct {
			profileID string
			snapshot  pipeline.BindingSnapshot
		}
		var options string
		if err := rows.Scan(&item.profileID, &item.snapshot.StageID, &item.snapshot.ProviderID,
			&item.snapshot.ModelID, &options, &item.snapshot.OptionsHash); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(options), &item.snapshot.Options); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// Get returns one profile with its bindings.
func (s *ProfileStore) Get(ctx context.Context, id string) (*Profile, error) {
	profiles, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for index := range profiles {
		if profiles[index].ID == id {
			return &profiles[index], nil
		}
	}
	return nil, ErrProfileNotFound
}

func (s *ProfileStore) validateProfile(name string, bindings []pipeline.BindingSnapshot) error {
	if err := pipeline.ValidateProfileName(name); err != nil {
		return err
	}
	if len(bindings) != len(pipeline.RegisteredStages()) {
		return fmt.Errorf("profile must bind exactly %d stages, got %d", len(pipeline.RegisteredStages()), len(bindings))
	}
	declaredTypes := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.ProviderType != config.ProviderTypeCodexAppServer && binding.ProviderType != config.ProviderTypeOpenAICompatible {
			return fmt.Errorf("binding %s has unknown provider type %q", binding.StageID, binding.ProviderType)
		}
		if binding.ProviderID != "" {
			if existing, ok := declaredTypes[binding.ProviderID]; ok && existing != binding.ProviderType {
				return fmt.Errorf("provider %q is declared with two different types", binding.ProviderID)
			}
			declaredTypes[binding.ProviderID] = binding.ProviderType
		}
	}
	// Re-canonicalizing through each binding's declared type verifies that
	// options match the provider type exactly (unknown fields, missing
	// fields, and out-of-range values all fail here).
	for _, binding := range bindings {
		if _, err := config.CanonicalizeProviderOptions(binding.ProviderType, binding.Options); err != nil {
			return fmt.Errorf("binding %s options: %w", binding.StageID, err)
		}
	}
	profile := pipeline.ProfileSnapshot{ID: "pending", Name: name, Bindings: bindings}
	return profile.Validate()
}

// Create stores a new profile. On duplicate names it returns a typed conflict.
func (s *ProfileStore) Create(ctx context.Context, name string, bindings []pipeline.BindingSnapshot) (*Profile, error) {
	if err := s.validateProfile(name, bindings); err != nil {
		return nil, err
	}
	profile := &Profile{ID: library.NewULID().String(), Name: strings.TrimSpace(name), Bindings: bindings}
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO analysis_pipeline_profile (id, name, updated_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`, profile.ID, profile.Name); err != nil {
			return writeProfileConflict(err)
		}
		return insertBindingsTx(ctx, tx, profile.ID, bindings)
	})
	if err != nil {
		return nil, err
	}
	return profile, nil
}

// Replace atomically replaces one profile's name and complete binding set.
func (s *ProfileStore) Replace(ctx context.Context, id, name string, bindings []pipeline.BindingSnapshot) (*Profile, error) {
	if err := s.validateProfile(name, bindings); err != nil {
		return nil, err
	}
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE analysis_pipeline_profile SET name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, strings.TrimSpace(name), id)
		if err != nil {
			return writeProfileConflict(err)
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrProfileNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM analysis_pipeline_binding WHERE profile_id = ?`, id); err != nil {
			return err
		}
		return insertBindingsTx(ctx, tx, id, bindings)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Rename changes only the name and may proceed while catalogs are stale.
func (s *ProfileStore) Rename(ctx context.Context, id, name string) (*Profile, error) {
	if err := pipeline.ValidateProfileName(name); err != nil {
		return nil, err
	}
	result, err := s.db.Exec(ctx, `UPDATE analysis_pipeline_profile SET name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, strings.TrimSpace(name), id)
	if err != nil {
		return nil, writeProfileConflict(err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return nil, ErrProfileNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes a profile unless it is the active one.
func (s *ProfileStore) Delete(ctx context.Context, id string) error {
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM analysis_pipeline_settings WHERE active_profile_id = ?`, id).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return &ProfileConflictError{Reason: "cannot delete the active profile"}
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM analysis_pipeline_profile WHERE id = ?`, id)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrProfileNotFound
		}
		return nil
	})
}

// Activate sets the singleton active profile.
func (s *ProfileStore) Activate(ctx context.Context, id string) error {
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM analysis_pipeline_profile WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrProfileNotFound
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO analysis_pipeline_settings (id, active_profile_id, updated_at) VALUES (1, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			ON CONFLICT(id) DO UPDATE SET active_profile_id = excluded.active_profile_id, updated_at = excluded.updated_at`, id)
		return err
	})
}

// ActiveProfile returns the active profile id or an empty string when unset.
func (s *ProfileStore) ActiveProfile(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT active_profile_id FROM analysis_pipeline_settings WHERE id = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// Count returns the number of stored profiles (for seeding decisions).
func (s *ProfileStore) Count(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM analysis_pipeline_profile`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// Seed inserts profiles only when none exist and activates the first.
func (s *ProfileStore) Seed(ctx context.Context, candidates []SeedProfile) ([]Profile, error) {
	count, err := s.Count(ctx)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, nil
	}
	created := make([]Profile, 0, len(candidates))
	for _, candidate := range candidates {
		profile, err := s.Create(ctx, candidate.Name, candidate.Bindings)
		if err != nil {
			return created, fmt.Errorf("seed profile %q: %w", candidate.Name, err)
		}
		created = append(created, *profile)
	}
	if len(created) > 0 {
		if err := s.Activate(ctx, created[0].ID); err != nil {
			return created, err
		}
	}
	return created, nil
}

// SeedProfile is one startup seeding candidate.
type SeedProfile struct {
	Name     string
	Bindings []pipeline.BindingSnapshot
}

func insertBindingsTx(ctx context.Context, tx *sql.Tx, profileID string, bindings []pipeline.BindingSnapshot) error {
	for _, binding := range bindings {
		options, err := json.Marshal(binding.Options)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO analysis_pipeline_binding (profile_id, stage_id, provider_id, model_id, options_json, options_hash) VALUES (?, ?, ?, ?, ?, ?)`,
			profileID, binding.StageID, binding.ProviderID, binding.ModelID, string(options), binding.OptionsHash); err != nil {
			return writeProfileConflict(err)
		}
	}
	return nil
}

func writeProfileConflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return &ProfileConflictError{Reason: "profile name already exists"}
	}
	return err
}

// ListProfileDescriptors is a small view used by the owner API tests.
func (s *ProfileStore) ListProfileDescriptors(ctx context.Context) ([]Profile, error) {
	return s.List(ctx)
}
