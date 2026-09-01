package speech

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

// Store owns reusable speech metadata and article-to-render bindings. Audio
// bytes are published by media.Store; this package only creates the metadata
// and the durable work needed to produce them.
type Store struct {
	db *store.DB
}

func NewStore(db *store.DB) *Store { return &Store{db: db} }

type UnitInput struct {
	Language                string
	UnitKind                string
	SpokenText              string
	ContextPronunciationKey string
	SemanticSenseID         *library.ULID
}

// EnsureUnitTx returns the identity-deduplicated speech unit for a transaction.
func EnsureUnitTx(ctx context.Context, tx *sql.Tx, input UnitInput) (*Unit, error) {
	if input.UnitKind != UnitWord && input.UnitKind != UnitPhrase && input.UnitKind != UnitSentence {
		return nil, fmt.Errorf("unsupported speech unit kind %q", input.UnitKind)
	}
	if strings.TrimSpace(input.Language) == "" {
		return nil, errors.New("speech unit language is required")
	}
	normalizedHash, err := NormalizeTextHash(input.SpokenText)
	if err != nil {
		return nil, err
	}
	var senseID any
	if input.SemanticSenseID != nil && !input.SemanticSenseID.IsZero() {
		senseID = input.SemanticSenseID.String()
	}
	find := func() (string, error) {
		var id string
		err := tx.QueryRowContext(ctx, `
		SELECT id FROM speech_unit
		WHERE language = ? AND unit_kind = ? AND normalized_text_hash = ?
		  AND context_pronunciation_key = ?
	`, input.Language, input.UnitKind, normalizedHash, input.ContextPronunciationKey).Scan(&id)
		return id, err
	}
	var rawID string
	rawID, err = find()
	if errors.Is(err, sql.ErrNoRows) {
		candidateID := library.NewULID().String()
		result, err := tx.ExecContext(ctx, `
			INSERT INTO speech_unit (id, language, unit_kind, spoken_text, normalized_text_hash, context_pronunciation_key, semantic_sense_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING
		`, candidateID, input.Language, input.UnitKind, input.SpokenText, normalizedHash, input.ContextPronunciationKey, senseID)
		if err != nil {
			return nil, fmt.Errorf("insert speech unit: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			rawID = candidateID
		} else if rawID, err = find(); err != nil {
			return nil, fmt.Errorf("find speech unit after conflict: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find speech unit: %w", err)
	}
	return getUnitTx(ctx, tx, rawID)
}

func getUnitTx(ctx context.Context, tx *sql.Tx, id string) (*Unit, error) {
	var unit Unit
	var rawSenseID sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT id, language, unit_kind, spoken_text, normalized_text_hash,
		       context_pronunciation_key, semantic_sense_id, created_at
		FROM speech_unit WHERE id = ?
	`, id).Scan(&unit.ID, &unit.Language, &unit.UnitKind, &unit.SpokenText, &unit.NormalizedTextHash, &unit.ContextPronunciationKey, &rawSenseID, &unit.CreatedAt); err != nil {
		return nil, err
	}
	if rawSenseID.Valid {
		value := library.ULID(rawSenseID.String)
		unit.SemanticSenseID = &value
	}
	return &unit, nil
}

func (s *Store) GetUnit(ctx context.Context, id library.ULID) (*Unit, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("speech: nil database")
	}
	return getUnit(ctx, s.db, id.String())
}

func getUnit(ctx context.Context, db *store.DB, id string) (*Unit, error) {
	var unit Unit
	var rawSenseID sql.NullString
	if err := db.QueryRow(ctx, `SELECT id, language, unit_kind, spoken_text, normalized_text_hash, context_pronunciation_key, semantic_sense_id, created_at FROM speech_unit WHERE id = ?`, id).Scan(&unit.ID, &unit.Language, &unit.UnitKind, &unit.SpokenText, &unit.NormalizedTextHash, &unit.ContextPronunciationKey, &rawSenseID, &unit.CreatedAt); err != nil {
		return nil, err
	}
	if rawSenseID.Valid {
		value := library.ULID(rawSenseID.String)
		unit.SemanticSenseID = &value
	}
	return &unit, nil
}

// DefaultProfiles creates immutable local defaults only when an active profile
// is absent. Real voice/model revisions can be installed as new profiles.
func DefaultProfilesTx(ctx context.Context, tx *sql.Tx, language string) (avspeech, chatterbox *Profile, err error) {
	if _, err := canonicalSpeechLanguage(language); err != nil {
		return nil, nil, err
	}
	avspeech, err = ensureProfileTx(ctx, tx, Profile{
		Engine: AVSpeechEngine, ModelRevision: AVSpeechModelRevision, Language: "nl",
		VoiceIdentifier: AVSpeechVoiceIdentifier, MappingVersion: AVSpeechMappingVersion,
		MIMEType: AudioMIME, Codec: AudioCodec, SampleRateHz: AudioSampleRate, Channels: AudioChannels,
		SpeedMilli: 1000, PitchCents: 0, Active: true,
	})
	if err != nil {
		return nil, nil, err
	}
	chatterbox, err = ensureProfileTx(ctx, tx, Profile{
		Engine: ChatterboxEngine, ModelRevision: ChatterboxModelRevision, Language: "nl",
		VoiceIdentifier: ChatterboxVoiceIdentifier, ReferenceAudioHash: ChatterboxReferenceAudioHash,
		MappingVersion: ChatterboxMappingVersion,
		MIMEType:       AudioMIME, Codec: AudioCodec, SampleRateHz: AudioSampleRate, Channels: AudioChannels,
		SpeedMilli: 1000, PitchCents: 0, Active: true,
	})
	return avspeech, chatterbox, err
}

func canonicalSpeechLanguage(language string) (string, error) {
	original := language
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "nl" || strings.HasPrefix(language, "nl-") {
		return "nl", nil
	}
	return "", fmt.Errorf("speech profiles are only available for Dutch, got %q", original)
}

func ensureProfileTx(ctx context.Context, tx *sql.Tx, profile Profile) (*Profile, error) {
	if err := ValidateProfile(profile); err != nil {
		return nil, err
	}
	var rawID string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM speech_profile
		WHERE engine = ? AND model_revision = ? AND language = ? AND voice_identifier = ?
		  AND reference_audio_hash = ? AND speed_milli = ? AND pitch_cents = ?
		  AND mapping_version = ? AND mime_type = ? AND codec = ? AND sample_rate_hz = ? AND channels = ?
		  AND active = 1
		ORDER BY created_at DESC, id DESC LIMIT 1
	`, profile.Engine, profile.ModelRevision, profile.Language, profile.VoiceIdentifier, profile.ReferenceAudioHash, profile.SpeedMilli, profile.PitchCents, profile.MappingVersion, profile.MIMEType, profile.Codec, profile.SampleRateHz, profile.Channels).Scan(&rawID)
	if errors.Is(err, sql.ErrNoRows) {
		rawID = library.NewULID().String()
		now := store.NowUTC()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO speech_profile (id, engine, model_revision, language, voice_identifier, reference_audio_hash, speed_milli, pitch_cents, mapping_version, mime_type, codec, sample_rate_hz, channels, active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, rawID, profile.Engine, profile.ModelRevision, profile.Language, profile.VoiceIdentifier, profile.ReferenceAudioHash, profile.SpeedMilli, profile.PitchCents, profile.MappingVersion, profile.MIMEType, profile.Codec, profile.SampleRateHz, profile.Channels, now, now); err != nil {
			return nil, fmt.Errorf("insert speech profile: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find speech profile: %w", err)
	}
	return getProfileTx(ctx, tx, rawID)
}

func getProfileTx(ctx context.Context, tx *sql.Tx, id string) (*Profile, error) {
	var profile Profile
	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT id, engine, model_revision, language, voice_identifier, reference_audio_hash,
		       speed_milli, pitch_cents, mapping_version, mime_type, codec, sample_rate_hz,
		       channels, active, created_at, updated_at
		FROM speech_profile WHERE id = ?
	`, id).Scan(&profile.ID, &profile.Engine, &profile.ModelRevision, &profile.Language, &profile.VoiceIdentifier, &profile.ReferenceAudioHash, &profile.SpeedMilli, &profile.PitchCents, &profile.MappingVersion, &profile.MIMEType, &profile.Codec, &profile.SampleRateHz, &profile.Channels, &active, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return nil, err
	}
	profile.Active = active != 0
	return &profile, nil
}

func (s *Store) GetRender(ctx context.Context, id library.ULID) (*Render, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("speech: nil database")
	}
	return getRender(ctx, s.db, id.String())
}

func getRender(ctx context.Context, db *store.DB, id string) (*Render, error) {
	var render Render
	if err := db.QueryRow(ctx, `SELECT id, speech_unit_id, speech_profile_id, request_hash, retention_class, state, error_code, duration_ms, size_bytes, created_at, updated_at, ready_at, COALESCE((SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = audio_render.id), '') FROM audio_render WHERE id = ?`, id).Scan(&render.ID, &render.SpeechUnitID, &render.SpeechProfileID, &render.RequestHash, &render.RetentionClass, &render.State, &render.ErrorCode, &render.DurationMS, &render.SizeBytes, &render.CreatedAt, &render.UpdatedAt, &render.ReadyAt, &render.BlobDigest); err != nil {
		return nil, err
	}
	return &render, nil
}

// EnsureRenderTx creates or returns the immutable request identity. Purged
// narration tombstones may be reactivated with the same request hash.
func EnsureRenderTx(ctx context.Context, tx *sql.Tx, unit Unit, profile Profile, retention string, reactivatePurged bool) (*Render, error) {
	if retention != RetentionLexical && retention != RetentionNarration {
		return nil, errors.New("invalid audio retention class")
	}
	requestHash := RequestHash(unit, profile)
	var rawID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM audio_render WHERE request_hash = ?`, requestHash).Scan(&rawID)
	if errors.Is(err, sql.ErrNoRows) {
		rawID = library.NewULID().String()
		now := store.NowUTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO audio_render (id, speech_unit_id, speech_profile_id, request_hash, retention_class, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?)`, rawID, unit.ID.String(), profile.ID.String(), requestHash, retention, now, now); err != nil {
			return nil, fmt.Errorf("insert audio render: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find audio render: %w", err)
	} else {
		var existingUnit, existingProfile, existingRetention, existingState string
		if err := tx.QueryRowContext(ctx, `SELECT speech_unit_id, speech_profile_id, retention_class, state FROM audio_render WHERE id = ?`, rawID).Scan(&existingUnit, &existingProfile, &existingRetention, &existingState); err != nil {
			return nil, err
		}
		if existingUnit != unit.ID.String() || existingProfile != profile.ID.String() || existingRetention != retention {
			return nil, errors.New("audio request hash collision with different render identity")
		}
		if existingState == RenderFailed || (reactivatePurged && existingState == RenderPurged) {
			if _, err := tx.ExecContext(ctx, `UPDATE audio_render SET state = 'queued', error_code = '', ready_at = '', updated_at = ? WHERE id = ? AND state IN ('purged', 'failed')`, store.NowUTC(), rawID); err != nil {
				return nil, err
			}
		}
	}
	return getRenderTx(ctx, tx, rawID)
}

func getRenderTx(ctx context.Context, tx *sql.Tx, id string) (*Render, error) {
	var render Render
	if err := tx.QueryRowContext(ctx, `SELECT id, speech_unit_id, speech_profile_id, request_hash, retention_class, state, error_code, duration_ms, size_bytes, created_at, updated_at, ready_at, COALESCE((SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = audio_render.id), '') FROM audio_render WHERE id = ?`, id).Scan(&render.ID, &render.SpeechUnitID, &render.SpeechProfileID, &render.RequestHash, &render.RetentionClass, &render.State, &render.ErrorCode, &render.DurationMS, &render.SizeBytes, &render.CreatedAt, &render.UpdatedAt, &render.ReadyAt, &render.BlobDigest); err != nil {
		return nil, err
	}
	return &render, nil
}

// QueueArticleAudio is idempotent. It binds lexical pronunciation renders and
// ordered sentence renders without waiting for a worker to be online.
func (s *Store) QueueArticleAudio(ctx context.Context, articleID library.ULID, reactivatePurged bool) error {
	if s == nil || s.db == nil {
		return errors.New("speech: nil database")
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		return QueueArticleAudioTx(ctx, tx, articleID, reactivatePurged)
	})
}

// QueueArticleAudioTx is the transaction-boundary variant used by semantic
// persistence so accepted analysis and its durable speech work become visible
// together.
func QueueArticleAudioTx(ctx context.Context, tx *sql.Tx, articleID library.ULID, reactivatePurged bool) error {
	if tx == nil {
		return errors.New("speech: nil transaction")
	}
	{
		var language string
		if err := tx.QueryRowContext(ctx, `SELECT source_language FROM article WHERE id = ?`, articleID.String()).Scan(&language); err != nil {
			return err
		}
		canonicalLanguage, err := canonicalSpeechLanguage(language)
		if err != nil {
			return err
		}
		language = canonicalLanguage
		avProfile, narrationProfile, err := DefaultProfilesTx(ctx, tx, language)
		if err != nil {
			return err
		}
		occurrenceRows, err := tx.QueryContext(ctx, `
			SELECT o.id, o.kind, o.role, o.semantic_sense_id,
			       (SELECT GROUP_CONCAT(sp.source_text, ' ') FROM article_occurrence_span sp WHERE sp.article_occurrence_id = o.id ORDER BY sp.span_index),
			       COALESCE(NULLIF(o.canonical_pronunciation_text, ''), COALESCE(s.canonical_pronunciation_text, '')),
			       o.context_pronunciation_key
			FROM article_occurrence o
			JOIN article_block b ON b.id = o.article_block_id
			LEFT JOIN semantic_sense s ON s.id = o.semantic_sense_id
			WHERE b.article_id = ? AND o.role IN ('token', 'contiguous_construction')
			ORDER BY b.block_index, o.id
		`, articleID.String())
		if err != nil {
			return err
		}
		type occurrence struct {
			id, kind, role, text, pronunciation, contextKey string
			senseID                                         sql.NullString
		}
		var occurrences []occurrence
		for occurrenceRows.Next() {
			var value occurrence
			if err := occurrenceRows.Scan(&value.id, &value.kind, &value.role, &value.senseID, &value.text, &value.pronunciation, &value.contextKey); err != nil {
				occurrenceRows.Close()
				return err
			}
			occurrences = append(occurrences, value)
		}
		if err := occurrenceRows.Err(); err != nil {
			occurrenceRows.Close()
			return err
		}
		occurrenceRows.Close()
		for _, occurrence := range occurrences {
			unitKind := UnitWord
			priority := 100
			jobType := jobs.AVSpeechJobType
			if occurrence.role == "contiguous_construction" {
				unitKind = UnitPhrase
				priority = 90
			}
			if unitKind == UnitPhrase && (utf8.RuneCountInString(occurrence.text) > 80 || len(strings.Fields(occurrence.text)) > 8) {
				continue
			}
			var senseID *library.ULID
			if occurrence.senseID.Valid {
				value := library.ULID(occurrence.senseID.String)
				senseID = &value
			}
			spokenText := occurrence.text
			if occurrence.pronunciation != "" {
				spokenText = occurrence.pronunciation
			}
			unit, err := EnsureUnitTx(ctx, tx, UnitInput{Language: language, UnitKind: unitKind, SpokenText: spokenText, ContextPronunciationKey: occurrence.contextKey, SemanticSenseID: senseID})
			if err != nil {
				return err
			}
			render, err := EnsureRenderTx(ctx, tx, *unit, *avProfile, RetentionLexical, false)
			if err != nil {
				return err
			}
			if err := ensureSpeechJobTx(ctx, tx, *render, *unit, *avProfile, jobType, priority); err != nil {
				return err
			}
			// A new immutable profile creates a new render for the same
			// occurrence. Keep old bindings for history, but expose exactly one
			// preferred pronunciation to the reader.
			if _, err := tx.ExecContext(ctx, `UPDATE article_occurrence_audio SET preferred = 0 WHERE article_occurrence_id = ? AND purpose = 'pronunciation'`, occurrence.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO article_occurrence_audio (article_occurrence_id, audio_render_id, purpose, preferred) VALUES (?, ?, 'pronunciation', 1) ON CONFLICT(article_occurrence_id, audio_render_id, purpose) DO UPDATE SET preferred = excluded.preferred`, occurrence.id, render.ID.String()); err != nil {
				return err
			}
		}

		sentenceRows, err := tx.QueryContext(ctx, `SELECT s.id, s.sentence_index, s.source_text FROM article_sentence s JOIN article_block b ON b.id = s.article_block_id WHERE b.article_id = ? ORDER BY b.block_index, s.sentence_index`, articleID.String())
		if err != nil {
			return err
		}
		type sentence struct {
			id, text string
			index    int
		}
		var sentences []sentence
		sequence := 0
		for sentenceRows.Next() {
			var value sentence
			if err := sentenceRows.Scan(&value.id, &value.index, &value.text); err != nil {
				sentenceRows.Close()
				return err
			}
			value.index = sequence
			sequence++
			sentences = append(sentences, value)
		}
		if err := sentenceRows.Err(); err != nil {
			sentenceRows.Close()
			return err
		}
		sentenceRows.Close()
		for sequence, sentence := range sentences {
			unit, err := EnsureUnitTx(ctx, tx, UnitInput{Language: language, UnitKind: UnitSentence, SpokenText: sentence.text})
			if err != nil {
				return err
			}
			render, err := EnsureRenderTx(ctx, tx, *unit, *narrationProfile, RetentionNarration, reactivatePurged)
			if err != nil {
				return err
			}
			priority := 50
			if sequence == 0 {
				priority = 70
			}
			if err := ensureSpeechJobTx(ctx, tx, *render, *unit, *narrationProfile, jobs.ChatterboxJobType, priority); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO article_sentence_audio (article_id, article_sentence_id, audio_render_id, sequence_index, purpose)
				VALUES (?, ?, ?, ?, 'narration')
				ON CONFLICT(article_sentence_id, purpose) DO UPDATE SET article_id = excluded.article_id, audio_render_id = excluded.audio_render_id, sequence_index = excluded.sequence_index
			`, articleID.String(), sentence.id, render.ID.String(), sequence); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE article SET narration_error_code = '', updated_at = ? WHERE id = ?`, store.NowUTC(), articleID.String()); err != nil {
			return err
		}
		return recomputeNarrationStatusTx(ctx, tx, articleID.String())
	}
}

func ensureSpeechJobTx(ctx context.Context, tx *sql.Tx, render Render, unit Unit, profile Profile, jobType string, priority int) error {
	payload, err := json.Marshal(JobPayload{ProtocolVersion: ProtocolVersion, RenderID: render.ID.String(), RequestHash: render.RequestHash, SpeechUnitID: unit.ID.String(), JobType: jobType, Language: unit.Language, UnitKind: unit.UnitKind, SpokenText: unit.SpokenText, ContextPronunciationKey: unit.ContextPronunciationKey, Profile: profile, Limits: Limits(unit.UnitKind)})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE job SET state = 'queued', attempt_count = 0, error_code = '', available_at = ?,
			lease_owner = '', lease_token_hash = '', lease_expires_at = '', completed_at = '', updated_at = ?
		WHERE idempotency_key = ? AND (
			state IN ('canceled', 'failed') OR (state = 'succeeded' AND ? = 'queued')
		)
	`, store.NowUTC(), store.NowUTC(), "speech.render:"+render.RequestHash, render.State); err != nil {
		return err
	}
	_, err = jobs.EnqueueTx(ctx, tx, jobs.Spec{
		JobType: jobType, ExecutionTarget: jobs.TargetMacOS, OwnerType: "audio_render", OwnerID: render.ID.String(),
		IdempotencyKey: "speech.render:" + render.RequestHash, InputHash: render.RequestHash, PayloadJSON: string(payload), Priority: priority,
	})
	return err
}

type JobPayload struct {
	ProtocolVersion         string      `json:"protocol_version"`
	RenderID                string      `json:"render_id"`
	RequestHash             string      `json:"request_hash"`
	SpeechUnitID            string      `json:"speech_unit_id"`
	JobType                 string      `json:"job_type"`
	Language                string      `json:"language"`
	UnitKind                string      `json:"unit_kind"`
	SpokenText              string      `json:"spoken_text"`
	ContextPronunciationKey string      `json:"context_pronunciation_key"`
	Profile                 Profile     `json:"profile"`
	Limits                  AudioLimits `json:"limits"`
	ExpiresAt               string      `json:"expires_at,omitempty"`
}

type WorkerCapability struct {
	Engine        string   `json:"engine"`
	Languages     []string `json:"languages"`
	UnitKinds     []string `json:"unit_kinds"`
	MaxBytes      int64    `json:"max_bytes"`
	MaxDurationMS int64    `json:"max_duration_ms"`
}

// SetRenderGenerating validates the identity before a trusted worker begins
// rendering. It is deliberately safe to repeat for the same render.
func (s *Store) SetRenderGenerating(ctx context.Context, renderID library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("speech: nil database")
	}
	result, err := s.db.Exec(ctx, `UPDATE audio_render SET state = 'generating', error_code = '', updated_at = ? WHERE id = ? AND state IN ('queued', 'failed', 'generating')`, store.NowUTC(), renderID.String())
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		var state string
		if err := s.db.QueryRow(ctx, `SELECT state FROM audio_render WHERE id = ?`, renderID.String()).Scan(&state); err != nil {
			return err
		}
		if state == RenderReady {
			return nil
		}
		return fmt.Errorf("audio render %s is not queueable", renderID.String())
	}
	return nil
}

func (s *Store) MarkRenderFailed(ctx context.Context, renderID library.ULID, code string) error {
	if !validSpeechErrorCode(code) {
		return errors.New("invalid speech error code")
	}
	_, err := s.db.Exec(ctx, `UPDATE audio_render SET state = 'failed', error_code = ?, updated_at = ? WHERE id = ? AND state IN ('queued', 'generating')`, code, store.NowUTC(), renderID.String())
	return err
}

func validSpeechErrorCode(code string) bool {
	if !strings.HasPrefix(code, "v1.") || len(code) > 120 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// MarkRenderReady binds the immutable blob digest and render metadata in one
// transaction. The media package inserts the blob row before this callback.
func MarkRenderReadyTx(ctx context.Context, tx *sql.Tx, renderID library.ULID, requestHash, digest string, metadata ArtifactMetadata) error {
	if metadata.RequestHash != requestHash {
		return errors.New("audio request hash mismatch")
	}
	var existingHash, state string
	if err := tx.QueryRowContext(ctx, `SELECT request_hash, state FROM audio_render WHERE id = ?`, renderID.String()).Scan(&existingHash, &state); err != nil {
		return err
	}
	if existingHash != requestHash {
		return errors.New("audio render request identity mismatch")
	}
	var oldDigest string
	err := tx.QueryRowContext(ctx, `SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = ?`, renderID.String()).Scan(&oldDigest)
	if err == nil {
		if oldDigest != digest {
			return &NondeterministicResultError{RenderID: renderID, ExistingDigest: oldDigest, NewDigest: digest}
		}
		if state != RenderReady {
			if _, err := tx.ExecContext(ctx, `UPDATE audio_render SET state = 'ready', error_code = '', duration_ms = ?, size_bytes = ?, ready_at = COALESCE(NULLIF(ready_at, ''), ?), updated_at = ? WHERE id = ? AND request_hash = ?`, metadata.DurationMS, metadata.SizeBytes, store.NowUTC(), store.NowUTC(), renderID.String(), requestHash); err != nil {
				return err
			}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if state == RenderReady && oldDigest == "" {
		return errors.New("ready audio render has no blob reference")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audio_blob_reference (audio_render_id, blob_digest) VALUES (?, ?)`, renderID.String(), digest); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE audio_render SET state = 'ready', error_code = '', duration_ms = ?, size_bytes = ?, ready_at = ?, updated_at = ? WHERE id = ? AND request_hash = ? AND state IN ('queued', 'generating', 'ready')`, metadata.DurationMS, metadata.SizeBytes, store.NowUTC(), store.NowUTC(), renderID.String(), requestHash)
	return err
}

type NondeterministicResultError struct {
	RenderID       library.ULID
	ExistingDigest string
	NewDigest      string
}

func (e *NondeterministicResultError) Error() string {
	return fmt.Sprintf("audio render %s has a different accepted digest", e.RenderID.String())
}

func (s *Store) RecomputeNarrationStatus(ctx context.Context, articleID library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("speech: nil database")
	}
	return s.db.WithTransaction(ctx, func(tx *sql.Tx) error { return recomputeNarrationStatusTx(ctx, tx, articleID.String()) })
}

func recomputeNarrationStatusTx(ctx context.Context, tx *sql.Tx, articleID string) error {
	var total, ready, failed, queued, generating int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN r.state = 'ready' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN r.state = 'failed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN r.state = 'queued' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN r.state = 'generating' THEN 1 ELSE 0 END), 0)
		FROM article_sentence_audio a JOIN audio_render r ON r.id = a.audio_render_id
		WHERE a.article_id = ?
	`, articleID).Scan(&total, &ready, &failed, &queued, &generating); err != nil {
		return err
	}
	status := string(NarrationNotRequested)
	if total > 0 {
		switch {
		case ready == total:
			status = NarrationReady
		case failed > 0 && ready == 0 && queued == 0 && generating == 0:
			status = NarrationFailed
		case ready > 0:
			status = NarrationPartial
		default:
			status = NarrationQueued
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE article SET narration_status = ?, updated_at = ? WHERE id = ?`, status, store.NowUTC(), articleID)
	return err
}

type NarrationResult struct {
	Narration
	RetainedBytes int64 `json:"retained_bytes"`
}

type ClearResult struct {
	ArticleID         library.ULID `json:"article_id"`
	SentenceCount     int          `json:"sentence_count"`
	ReclaimedBytes    int64        `json:"reclaimed_bytes"`
	RetainedBytes     int64        `json:"retained_bytes"`
	PurgedRenderCount int          `json:"purged_render_count"`
	CleanupDigests    []string     `json:"-"`
	Status            string       `json:"status"`
}

// ClearNarration removes only article-narration bindings. Render rows remain
// as purged tombstones and lexical renders are never included in the query.
// CleanupDigests are safe to pass to media.Store.CleanupOrphan after commit.
func (s *Store) ClearNarration(ctx context.Context, articleID library.ULID) (*ClearResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("speech: nil database")
	}
	result := &ClearResult{ArticleID: articleID, Status: NarrationPurged, CleanupDigests: []string{}}
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM article WHERE id = ?`, articleID.String()).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT a.audio_render_id, r.size_bytes,
			       COALESCE((SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = a.audio_render_id), '')
			FROM article_sentence_audio a JOIN audio_render r ON r.id = a.audio_render_id
			WHERE a.article_id = ? AND r.retention_class = 'article_narration'
		`, articleID.String())
		if err != nil {
			return err
		}
		type renderInfo struct {
			id, digest string
			size       int64
		}
		var renders []renderInfo
		for rows.Next() {
			var info renderInfo
			if err := rows.Scan(&info.id, &info.size, &info.digest); err != nil {
				rows.Close()
				return err
			}
			renders = append(renders, info)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM article_sentence_audio WHERE article_id = ?`, articleID.String()).Scan(&result.SentenceCount); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM article_sentence_audio WHERE article_id = ?`, articleID.String()); err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(renders))
		for _, info := range renders {
			if _, ok := seen[info.id]; ok {
				continue
			}
			seen[info.id] = struct{}{}
			var references int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM article_sentence_audio WHERE audio_render_id = ?`, info.id).Scan(&references); err != nil {
				return err
			}
			if references == 0 {
				if _, err := jobs.CancelOwnerJobsTx(ctx, tx, "audio_render", info.id, jobs.ChatterboxJobType, "v1.narration_cleared"); err != nil {
					return err
				}
				if info.digest != "" {
					if _, err := tx.ExecContext(ctx, `DELETE FROM audio_blob_reference WHERE audio_render_id = ?`, info.id); err != nil {
						return err
					}
					result.CleanupDigests = append(result.CleanupDigests, info.digest)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE audio_render SET state = 'purged', error_code = '', updated_at = ? WHERE id = ?`, store.NowUTC(), info.id); err != nil {
					return err
				}
				result.ReclaimedBytes += info.size
				result.PurgedRenderCount++
			} else {
				result.RetainedBytes += info.size
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE article SET narration_status = 'purged', narration_error_code = '', updated_at = ? WHERE id = ?`, store.NowUTC(), articleID.String())
		return err
	})
	return result, err
}

func (s *Store) GetNarration(ctx context.Context, articleID library.ULID) (*Narration, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("speech: nil database")
	}
	var narration Narration
	narration.ArticleID = articleID
	if err := s.db.QueryRow(ctx, `SELECT narration_status, narration_error_code FROM article WHERE id = ?`, articleID.String()).Scan(&narration.Status, &narration.ErrorCode); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT s.id, r.id, r.state, r.duration_ms, r.size_bytes, r.error_code
		FROM article_sentence s
		JOIN article_block b ON b.id = s.article_block_id
		LEFT JOIN article_sentence_audio a ON a.article_sentence_id = s.id AND a.article_id = ?
		LEFT JOIN audio_render r ON r.id = a.audio_render_id
		WHERE b.article_id = ?
		ORDER BY b.block_index, s.sentence_index
	`, articleID.String(), articleID.String())
	if err != nil {
		return nil, err
	}
	narration.Clips = make([]NarrationClip, 0)
	for rows.Next() {
		var clip NarrationClip
		var renderID, state, errorCode sql.NullString
		var duration, size sql.NullInt64
		if err := rows.Scan(&clip.SentenceID, &renderID, &state, &duration, &size, &errorCode); err != nil {
			rows.Close()
			return nil, err
		}
		clip.SequenceIndex = narration.SentenceCount
		if renderID.Valid {
			render := library.ULID(renderID.String)
			clip.Audio = &AudioRef{RenderID: render, Ready: state.Valid && state.String == RenderReady, DurationMS: duration.Int64, SizeBytes: size.Int64, ErrorCode: errorCode.String}
			if clip.Audio.Ready {
				narration.ReadyCount++
				narration.DurationMS += clip.Audio.DurationMS
				narration.SizeBytes += clip.Audio.SizeBytes
			}
		}
		narration.SentenceCount++
		narration.Clips = append(narration.Clips, clip)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(refs.size_bytes), 0)
		FROM (
			SELECT DISTINCT a.audio_render_id, r.size_bytes
			FROM article_sentence_audio a JOIN audio_render r ON r.id = a.audio_render_id
			WHERE a.article_id = ? AND r.retention_class = 'article_narration' AND r.state = 'ready'
			  AND NOT EXISTS (
				  SELECT 1 FROM article_sentence_audio other
				  WHERE other.audio_render_id = a.audio_render_id AND other.article_id <> ?
			  )
		) refs
	`, articleID.String(), articleID.String()).Scan(&narration.ReclaimableBytes); err != nil {
		return nil, err
	}
	return &narration, nil
}
