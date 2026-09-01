package analysis

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"doublangu/internal/store"
)

// Settings is the singleton owner selection used for newly queued analysis.
// A blank model means the server has not yet discovered or selected a usable
// provider model; the server remains up but analysis fails closed.
type Settings struct {
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	UpdatedAt string `json:"updated_at"`
}

// SettingsStore persists the owner selection independently of environment
// configuration. Environment values are only used by Seed.
type SettingsStore struct {
	db *store.DB
}

func NewSettingsStore(db *store.DB) *SettingsStore { return &SettingsStore{db: db} }

// Seed inserts the initial selection only when the singleton has no row. A
// later process restart never overwrites an owner change from the environment.
func (s *SettingsStore) Seed(ctx context.Context, model, effort string) error {
	if s == nil || s.db == nil {
		return errors.New("analysis settings: nil database")
	}
	if strings.TrimSpace(effort) == "" {
		effort = "medium"
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_settings (id, model, effort, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, strings.TrimSpace(model), strings.TrimSpace(effort), store.NowUTC())
	return err
}

func (s *SettingsStore) Get(ctx context.Context) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{}, errors.New("analysis settings: nil database")
	}
	var settings Settings
	err := s.db.QueryRow(ctx, `SELECT model, effort, updated_at FROM analysis_settings WHERE id = 1`).Scan(&settings.Model, &settings.Effort, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{Effort: "medium"}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get analysis settings: %w", err)
	}
	return settings, nil
}

func (s *SettingsStore) Save(ctx context.Context, model, effort string) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{}, errors.New("analysis settings: nil database")
	}
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" || effort == "" {
		return Settings{}, errors.New("analysis model and effort are required")
	}
	updatedAt := store.NowUTC()
	_, err := s.db.Exec(ctx, `
		INSERT INTO analysis_settings (id, model, effort, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET model = excluded.model, effort = excluded.effort, updated_at = excluded.updated_at
	`, model, effort, updatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("save analysis settings: %w", err)
	}
	return Settings{Model: model, Effort: effort, UpdatedAt: updatedAt}, nil
}
