package reader

import (
	"context"
	"database/sql"
	"errors"

	"doublangu/internal/store"
)

// ReaderSettings is the owner-wide reader preference singleton. The server is
// authoritative; local storage only mirrors the last successful value.
type ReaderSettings struct {
	PronounceOnHover bool   `json:"pronounce_on_hover"`
	UpdatedAt        string `json:"updated_at"`
}

// DefaultReaderSettings applies when the singleton row is absent (defensive;
// migration 007 seeds the row with hover enabled).
func DefaultReaderSettings() ReaderSettings {
	return ReaderSettings{PronounceOnHover: true}
}

// GetReaderSettings returns the persisted owner preference.
func (s *Store) GetReaderSettings(ctx context.Context) (ReaderSettings, error) {
	if s == nil || s.db == nil {
		return ReaderSettings{}, errors.New("reader: nil database")
	}
	var settings ReaderSettings
	var hover int
	err := s.db.QueryRow(ctx, `SELECT pronounce_on_hover, updated_at FROM reader_settings WHERE id = 1`).Scan(&hover, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultReaderSettings(), nil
	}
	if err != nil {
		return ReaderSettings{}, err
	}
	settings.PronounceOnHover = hover != 0
	return settings, nil
}

// SetReaderSettings stores the owner preference and returns the persisted
// value with its update time.
func (s *Store) SetReaderSettings(ctx context.Context, pronounceOnHover bool) (ReaderSettings, error) {
	if s == nil || s.db == nil {
		return ReaderSettings{}, errors.New("reader: nil database")
	}
	settings := ReaderSettings{PronounceOnHover: pronounceOnHover, UpdatedAt: store.NowUTC()}
	hover := 0
	if pronounceOnHover {
		hover = 1
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO reader_settings (id, pronounce_on_hover, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET pronounce_on_hover = excluded.pronounce_on_hover, updated_at = excluded.updated_at
	`, hover, settings.UpdatedAt)
	if err != nil {
		return ReaderSettings{}, err
	}
	return settings, nil
}
