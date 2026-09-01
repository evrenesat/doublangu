// Package workers implements the server side of the outbound speech-worker
// protocol. It has no inbound socket or owner credential access; workers are
// independently enrolled and receive only bounded render payloads.
package workers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/media"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

const (
	EnrollmentLifetime = 30 * time.Minute
	LongPollLifetime   = 25 * time.Second
	MaxWorkerName      = 120
	MaxCapabilities    = 32
)

var (
	ErrUnauthorized     = errors.New("worker unauthorized")
	ErrProtocol         = errors.New("unsupported worker protocol")
	ErrNoWork           = jobs.ErrNoWork
	ErrMalformedJob     = errors.New("malformed speech job")
	ErrNondeterministic = errors.New("audio result is nondeterministic")
	ErrUploadRejected   = errors.New("audio upload rejected")
)

type Service struct {
	db     *store.DB
	jobs   *jobs.Store
	speech *speech.Store
	media  *media.Store
}

func NewService(db *store.DB, mediaStore *media.Store) *Service {
	return &Service{db: db, jobs: jobs.NewStore(db), speech: speech.NewStore(db), media: mediaStore}
}

type Enrollment struct {
	ID        library.ULID `json:"id"`
	Token     string       `json:"token"`
	ExpiresAt string       `json:"expires_at"`
}

type EnrollInput struct {
	Name            string                    `json:"name"`
	ProtocolVersion string                    `json:"protocol_version"`
	Capabilities    []speech.WorkerCapability `json:"capabilities"`
	SoftwareVersion string                    `json:"software_version"`
}

type Worker struct {
	ID              library.ULID              `json:"id"`
	Name            string                    `json:"name"`
	ProtocolVersion string                    `json:"protocol_version"`
	RevokedAt       string                    `json:"revoked_at"`
	LastSeenAt      string                    `json:"last_seen_at"`
	Capabilities    []speech.WorkerCapability `json:"capabilities"`
	SoftwareVersion string                    `json:"software_version"`
	CreatedAt       string                    `json:"created_at"`
	UpdatedAt       string                    `json:"updated_at"`
}

type LeaseRequest struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Capabilities    []speech.WorkerCapability `json:"capabilities"`
}

type LeaseResponse struct {
	ProtocolVersion         string             `json:"protocol_version"`
	JobID                   library.ULID       `json:"job_id"`
	Attempt                 int                `json:"attempt"`
	LeaseToken              string             `json:"lease_token"`
	LeaseExpiresAt          string             `json:"lease_expires_at"`
	JobType                 string             `json:"job_type"`
	RenderID                library.ULID       `json:"render_id"`
	RequestHash             string             `json:"request_hash"`
	SpeechUnitID            library.ULID       `json:"speech_unit_id"`
	Language                string             `json:"language"`
	UnitKind                string             `json:"unit_kind"`
	SpokenText              string             `json:"spoken_text"`
	ContextPronunciationKey string             `json:"context_pronunciation_key"`
	Profile                 speech.Profile     `json:"profile"`
	Limits                  speech.AudioLimits `json:"limits"`
}

type HeartbeatInput struct {
	ProtocolVersion string `json:"protocol_version"`
	Attempt         int    `json:"attempt"`
	ProgressPercent int    `json:"progress_percent"`
}

type HeartbeatResponse struct {
	ProtocolVersion string `json:"protocol_version"`
	CancelRequested bool   `json:"cancel_requested"`
	LeaseExpiresAt  string `json:"lease_expires_at"`
	ProgressPercent int    `json:"progress_percent"`
}

type FailInput struct {
	ProtocolVersion string `json:"protocol_version"`
	Attempt         int    `json:"attempt"`
	ErrorCode       string `json:"error_code"`
	Retry           bool   `json:"retry"`
}

type CompleteMetadata struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Attempt         int                     `json:"attempt"`
	LeaseToken      string                  `json:"lease_token"`
	Artifact        speech.ArtifactMetadata `json:"artifact"`
}

func (s *Service) CreateEnrollment(ctx context.Context) (*Enrollment, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workers: nil database")
	}
	token, err := randomSecret()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(EnrollmentLifetime).Format("2006-01-02T15:04:05.000Z")
	enrollment := &Enrollment{ID: library.NewULID(), Token: token, ExpiresAt: expires}
	_, err = s.db.Exec(ctx, `INSERT INTO speech_worker_enrollment (id, token_hash, expires_at) VALUES (?, ?, ?)`, enrollment.ID.String(), hashSecret(token), expires)
	if err != nil {
		return nil, err
	}
	return enrollment, nil
}

func (s *Service) ListWorkers(ctx context.Context) ([]Worker, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workers: nil database")
	}
	rows, err := s.db.Query(ctx, `SELECT id, name, protocol_version, revoked_at, last_seen_at, capabilities_json, software_version, created_at, updated_at FROM speech_worker ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Worker, 0)
	for rows.Next() {
		var worker Worker
		var capabilities string
		if err := rows.Scan(&worker.ID, &worker.Name, &worker.ProtocolVersion, &worker.RevokedAt, &worker.LastSeenAt, &capabilities, &worker.SoftwareVersion, &worker.CreatedAt, &worker.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(capabilities), &worker.Capabilities); err != nil {
			return nil, err
		}
		if worker.Capabilities == nil {
			worker.Capabilities = []speech.WorkerCapability{}
		}
		result = append(result, worker)
	}
	return result, rows.Err()
}

func (s *Service) Enroll(ctx context.Context, token string, input EnrollInput) (*Worker, string, error) {
	if s == nil || s.db == nil {
		return nil, "", errors.New("workers: nil database")
	}
	if err := validateEnrollInput(input); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(token) == "" {
		return nil, "", ErrUnauthorized
	}
	workerToken, err := randomSecret()
	if err != nil {
		return nil, "", err
	}
	capabilities, err := json.Marshal(input.Capabilities)
	if err != nil {
		return nil, "", err
	}
	var worker Worker
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var enrollmentID string
		var expiresAt, usedAt string
		rows, err := tx.QueryContext(ctx, `SELECT id, expires_at, used_at, token_hash FROM speech_worker_enrollment WHERE used_at = '' AND expires_at > ?`, store.NowUTC())
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var hash string
			var candidateID, candidateExpires, candidateUsed string
			if err := rows.Scan(&candidateID, &candidateExpires, &candidateUsed, &hash); err != nil {
				rows.Close()
				return err
			}
			if jobs.LeaseTokenMatches(token, hash) {
				enrollmentID, expiresAt, usedAt, found = candidateID, candidateExpires, candidateUsed, true
				break
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !found || expiresAt <= store.NowUTC() || usedAt != "" {
			return ErrUnauthorized
		}
		worker = Worker{ID: library.NewULID(), Name: strings.TrimSpace(input.Name), ProtocolVersion: input.ProtocolVersion, Capabilities: input.Capabilities, SoftwareVersion: strings.TrimSpace(input.SoftwareVersion), CreatedAt: store.NowUTC(), UpdatedAt: store.NowUTC()}
		if _, err := tx.ExecContext(ctx, `INSERT INTO speech_worker (id, name, protocol_version, token_hash, capabilities_json, software_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, worker.ID.String(), worker.Name, worker.ProtocolVersion, hashSecret(workerToken), string(capabilities), worker.SoftwareVersion, worker.CreatedAt, worker.UpdatedAt); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE speech_worker_enrollment SET used_at = ? WHERE id = ? AND used_at = ''`, store.NowUTC(), enrollmentID)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return &worker, workerToken, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (*Worker, error) {
	if s == nil || s.db == nil || strings.TrimSpace(token) == "" {
		return nil, ErrUnauthorized
	}
	rows, err := s.db.Query(ctx, `SELECT id, name, protocol_version, token_hash, revoked_at, last_seen_at, capabilities_json, software_version, created_at, updated_at FROM speech_worker WHERE revoked_at = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var worker *Worker
	for rows.Next() {
		var candidate Worker
		var tokenHash, capabilities string
		if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.ProtocolVersion, &tokenHash, &candidate.RevokedAt, &candidate.LastSeenAt, &capabilities, &candidate.SoftwareVersion, &candidate.CreatedAt, &candidate.UpdatedAt); err != nil {
			return nil, err
		}
		if jobs.LeaseTokenMatches(token, tokenHash) {
			if err := json.Unmarshal([]byte(capabilities), &candidate.Capabilities); err != nil {
				return nil, err
			}
			worker = &candidate
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, ErrUnauthorized
	}
	now := store.NowUTC()
	_, err = s.db.Exec(ctx, `UPDATE speech_worker SET last_seen_at = ?, updated_at = ? WHERE id = ? AND revoked_at = ''`, now, now, worker.ID.String())
	if err != nil {
		return nil, err
	}
	worker.LastSeenAt = now
	worker.UpdatedAt = now
	return worker, nil
}

func (s *Service) Revoke(ctx context.Context, id library.ULID) error {
	if s == nil || s.db == nil {
		return errors.New("workers: nil database")
	}
	now := store.NowUTC()
	if _, err := s.db.Exec(ctx, `UPDATE speech_worker SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at = ''`, now, now, id.String()); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE job SET state = 'queued', lease_owner = '', lease_token_hash = '', lease_expires_at = '', error_code = 'v1.worker_revoked', updated_at = ? WHERE lease_owner = ? AND state IN ('leased', 'running')`, now, id.String())
	return err
}

func (s *Service) Lease(ctx context.Context, worker *Worker, request LeaseRequest) (*LeaseResponse, error) {
	if worker == nil || worker.RevokedAt != "" {
		return nil, ErrUnauthorized
	}
	if request.ProtocolVersion != speech.ProtocolVersion {
		return nil, ErrProtocol
	}
	if err := validateCapabilities(request.Capabilities); err != nil {
		return nil, err
	}
	if len(worker.Capabilities) == 0 || !capabilitiesSubset(request.Capabilities, worker.Capabilities) {
		return nil, ErrProtocol
	}
	if _, err := s.jobs.RecoverExpired(ctx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(LongPollLifetime)
	for {
		lease, err := s.jobs.ClaimMatching(ctx, jobs.TargetMacOS, worker.ID.String(), func(job jobs.Job) bool {
			return supportsJob(job, request.Capabilities)
		})
		if err == nil {
			response, responseErr := s.leaseResponse(ctx, lease)
			if responseErr != nil {
				s.rejectMalformedLease(ctx, lease)
				return nil, responseErr
			}
			return response, nil
		}
		if !errors.Is(err, jobs.ErrNoWork) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrNoWork
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Service) leaseResponse(ctx context.Context, lease *jobs.Lease) (*LeaseResponse, error) {
	var payload speech.JobPayload
	if err := decodeStrict([]byte(lease.PayloadJSON), &payload); err != nil {
		return nil, ErrMalformedJob
	}
	if err := validateJobPayload(lease, payload); err != nil {
		return nil, ErrMalformedJob
	}
	renderID, err := library.ParseULID(payload.RenderID)
	if err != nil {
		return nil, ErrMalformedJob
	}
	unitID, err := library.ParseULID(payload.SpeechUnitID)
	if err != nil {
		return nil, ErrMalformedJob
	}
	if s == nil || s.db == nil {
		return nil, ErrMalformedJob
	}
	var storedUnitID, storedProfileID, storedHash, storedState string
	if err := s.db.QueryRow(ctx, `
		SELECT speech_unit_id, speech_profile_id, request_hash, state
		FROM audio_render WHERE id = ?
	`, payload.RenderID).Scan(&storedUnitID, &storedProfileID, &storedHash, &storedState); err != nil {
		return nil, ErrMalformedJob
	}
	if storedUnitID != payload.SpeechUnitID || storedProfileID != payload.Profile.ID.String() || storedHash != payload.RequestHash || (storedState != speech.RenderQueued && storedState != speech.RenderFailed && storedState != speech.RenderGenerating) {
		return nil, ErrMalformedJob
	}
	return &LeaseResponse{ProtocolVersion: speech.ProtocolVersion, JobID: lease.ID, Attempt: lease.AttemptCount, LeaseToken: lease.LeaseToken, LeaseExpiresAt: lease.LeaseExpiresAt, JobType: lease.JobType, RenderID: renderID, RequestHash: payload.RequestHash, SpeechUnitID: unitID, Language: payload.Language, UnitKind: payload.UnitKind, SpokenText: payload.SpokenText, ContextPronunciationKey: payload.ContextPronunciationKey, Profile: payload.Profile, Limits: payload.Limits}, nil
}

func (s *Service) rejectMalformedLease(ctx context.Context, lease *jobs.Lease) {
	if lease == nil {
		return
	}
	var payload speech.JobPayload
	if decodeStrict([]byte(lease.PayloadJSON), &payload) == nil {
		if renderID, err := library.ParseULID(payload.RenderID); err == nil && !renderID.IsZero() && s.speech != nil {
			_ = s.speech.MarkRenderFailed(ctx, renderID, "v1.worker_malformed_job")
			s.recomputeRenderArticles(ctx, renderID)
		}
	}
	_ = s.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, "v1.worker_malformed_job", false)
}

func (s *Service) Heartbeat(ctx context.Context, worker *Worker, jobID library.ULID, token string, input HeartbeatInput) (*HeartbeatResponse, error) {
	if worker == nil || input.ProtocolVersion != speech.ProtocolVersion {
		return nil, ErrUnauthorized
	}
	if _, err := s.jobs.VerifyLease(ctx, jobID, input.Attempt, token, worker.ID.String()); err != nil {
		if errors.Is(err, jobs.ErrLeaseExpired) || errors.Is(err, jobs.ErrLeaseLost) {
			return nil, err
		}
		return nil, err
	}
	result, err := s.jobs.Heartbeat(ctx, jobID, input.Attempt, token, input.ProgressPercent)
	if err != nil {
		return nil, err
	}
	return &HeartbeatResponse{ProtocolVersion: speech.ProtocolVersion, CancelRequested: result.CancelRequested, LeaseExpiresAt: result.Job.LeaseExpiresAt, ProgressPercent: result.Job.ProgressPercent}, nil
}

func (s *Service) Fail(ctx context.Context, worker *Worker, jobID library.ULID, token string, input FailInput) error {
	if worker == nil || input.ProtocolVersion != speech.ProtocolVersion || !validWorkerErrorCode(input.ErrorCode) {
		return ErrUploadRejected
	}
	if _, err := s.jobs.VerifyLease(ctx, jobID, input.Attempt, token, worker.ID.String()); err != nil {
		return err
	}
	if err := s.jobs.Fail(ctx, jobID, input.Attempt, token, input.ErrorCode, input.Retry); err != nil {
		return err
	}
	if !input.Retry {
		job, err := s.jobs.Get(ctx, jobID)
		if err == nil {
			var payload speech.JobPayload
			if decodeStrict([]byte(job.PayloadJSON), &payload) == nil {
				if renderID, parseErr := library.ParseULID(payload.RenderID); parseErr == nil && !renderID.IsZero() {
					_ = s.speech.MarkRenderFailed(ctx, renderID, input.ErrorCode)
					s.recomputeRenderArticles(ctx, renderID)
				}
			}
		}
	}
	return nil
}

func (s *Service) Complete(ctx context.Context, worker *Worker, jobID library.ULID, metadata CompleteMetadata, data []byte) error {
	if worker == nil || metadata.ProtocolVersion != speech.ProtocolVersion || s.media == nil {
		return ErrUnauthorized
	}
	job, err := s.jobs.VerifyLease(ctx, jobID, metadata.Attempt, metadata.LeaseToken, worker.ID.String())
	if err != nil {
		return err
	}
	if job.State == jobs.StateCanceled {
		return jobs.ErrLeaseLost
	}
	var payload speech.JobPayload
	if err := decodeStrict([]byte(job.PayloadJSON), &payload); err != nil {
		return ErrMalformedJob
	}
	if metadata.Artifact.RequestHash != payload.RequestHash || job.InputHash != payload.RequestHash {
		return ErrUploadRejected
	}
	if int64(len(data)) != metadata.Artifact.SizeBytes || len(data) == 0 {
		return ErrUploadRejected
	}
	digestBytes := sha256.Sum256(data)
	digest := hex.EncodeToString(digestBytes[:])
	if digest != metadata.Artifact.SHA256 {
		return ErrUploadRejected
	}
	if err := speech.ValidateArtifact(metadata.Artifact, int64(len(data)), data, payload.RequestHash, payload.UnitKind); err != nil {
		return ErrUploadRejected
	}
	renderID, err := library.ParseULID(payload.RenderID)
	if err != nil {
		return ErrMalformedJob
	}
	if err := s.speech.SetRenderGenerating(ctx, renderID); err != nil {
		return err
	}
	prepared, err := s.media.PrepareWrite(data)
	if err != nil {
		_ = s.speech.MarkRenderFailed(ctx, renderID, "v1.audio_upload_failed")
		_ = s.jobs.Fail(ctx, jobID, metadata.Attempt, metadata.LeaseToken, "v1.audio_upload_failed", true)
		return err
	}
	_, err = s.media.CommitPrepared(ctx, s.db, speech.AudioMIME, prepared, func(tx *sql.Tx, blobDigest string, _ int64) error {
		if err := speech.MarkRenderReadyTx(ctx, tx, renderID, payload.RequestHash, blobDigest, metadata.Artifact); err != nil {
			var nondeterministic *speech.NondeterministicResultError
			if errors.As(err, &nondeterministic) {
				return ErrNondeterministic
			}
			return err
		}
		return jobs.CompleteTx(ctx, tx, jobID, metadata.Attempt, metadata.LeaseToken)
	})
	if err != nil {
		var nondeterministic *speech.NondeterministicResultError
		if errors.Is(err, ErrNondeterministic) || errors.As(err, &nondeterministic) {
			// The accepted render remains immutable. If this was the first
			// completion race, leave a durable failure marker on the in-flight
			// render so it cannot remain stuck in generating after the rejected
			// artifact transaction rolls back.
			_ = s.speech.MarkRenderFailed(ctx, renderID, "v1.audio_nondeterministic_result")
			_ = s.jobs.Fail(ctx, jobID, metadata.Attempt, metadata.LeaseToken, "v1.audio_nondeterministic_result", false)
			return err
		}
		_ = s.speech.MarkRenderFailed(ctx, renderID, "v1.audio_upload_failed")
		_ = s.jobs.Fail(ctx, jobID, metadata.Attempt, metadata.LeaseToken, "v1.audio_upload_failed", true)
		return err
	}
	s.recomputeRenderArticles(ctx, renderID)
	return nil
}

func (s *Service) recomputeRenderArticles(ctx context.Context, renderID library.ULID) {
	articleRows, queryErr := s.db.Query(ctx, `SELECT DISTINCT article_id FROM article_sentence_audio WHERE audio_render_id = ?`, renderID.String())
	if queryErr != nil {
		return
	}
	articleIDs := make([]library.ULID, 0, 1)
	for articleRows.Next() {
		var articleID string
		if scanErr := articleRows.Scan(&articleID); scanErr == nil {
			articleIDs = append(articleIDs, library.ULID(articleID))
		}
	}
	_ = articleRows.Close()
	for _, articleID := range articleIDs {
		_ = s.speech.RecomputeNarrationStatus(ctx, articleID)
	}
}

func supportsJob(job jobs.Job, capabilities []speech.WorkerCapability) bool {
	var payload speech.JobPayload
	lease := &jobs.Lease{Job: job}
	if decodeStrict([]byte(job.PayloadJSON), &payload) != nil || validateJobPayload(lease, payload) != nil {
		return false
	}
	for _, capability := range capabilities {
		if capability.Engine != payload.Profile.Engine || (capability.MaxBytes > 0 && capability.MaxBytes < payload.Limits.MaxBytes) || (capability.MaxDurationMS > 0 && capability.MaxDurationMS < payload.Limits.MaxDurationMS) {
			continue
		}
		languageOK, kindOK := false, false
		for _, language := range capability.Languages {
			if language == payload.Language || language == "*" {
				languageOK = true
			}
		}
		for _, kind := range capability.UnitKinds {
			if kind == payload.UnitKind || kind == "*" {
				kindOK = true
			}
		}
		if languageOK && kindOK {
			return true
		}
	}
	return false
}

func validateJobPayload(lease *jobs.Lease, payload speech.JobPayload) error {
	if lease == nil || lease.JobType != payload.JobType || lease.ExecutionTarget != jobs.TargetMacOS || lease.OwnerType != "audio_render" || lease.OwnerID != payload.RenderID {
		return ErrMalformedJob
	}
	if payload.ProtocolVersion != speech.ProtocolVersion || payload.RequestHash != lease.InputHash || !isLowerHexDigest(payload.RequestHash) {
		return ErrMalformedJob
	}
	if payload.JobType != jobs.AVSpeechJobType && payload.JobType != jobs.ChatterboxJobType {
		return ErrMalformedJob
	}
	expectedEngine := speech.AVSpeechEngine
	if payload.JobType == jobs.ChatterboxJobType {
		expectedEngine = speech.ChatterboxEngine
	}
	if payload.Profile.Engine != expectedEngine || payload.Profile.Language != payload.Language {
		return ErrMalformedJob
	}
	if id, err := library.ParseULID(payload.RenderID); err != nil || id.IsZero() {
		return ErrMalformedJob
	}
	if id, err := library.ParseULID(payload.SpeechUnitID); err != nil || id.IsZero() {
		return ErrMalformedJob
	}
	if id, err := library.ParseULID(payload.Profile.ID.String()); err != nil || id.IsZero() {
		return ErrMalformedJob
	}
	language, err := library.ParseBCP47(payload.Language)
	if err != nil || language != payload.Language {
		return ErrMalformedJob
	}
	if payload.UnitKind != speech.UnitWord && payload.UnitKind != speech.UnitPhrase && payload.UnitKind != speech.UnitSentence {
		return ErrMalformedJob
	}
	if payload.Limits != speech.Limits(payload.UnitKind) {
		return ErrMalformedJob
	}
	if payload.Limits.MaxBytes <= 0 || payload.Limits.MaxDurationMS <= 0 || strings.TrimSpace(payload.SpokenText) == "" {
		return ErrMalformedJob
	}
	if _, err := speech.NormalizeTextHash(payload.SpokenText); err != nil {
		return ErrMalformedJob
	}
	if err := speech.ValidateProfile(payload.Profile); err != nil {
		return ErrMalformedJob
	}
	return nil
}

func isLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func validateEnrollInput(input EnrollInput) error {
	if input.ProtocolVersion != speech.ProtocolVersion || strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > MaxWorkerName {
		return ErrProtocol
	}
	return validateCapabilities(input.Capabilities)
}

func validateCapabilities(capabilities []speech.WorkerCapability) error {
	if len(capabilities) == 0 || len(capabilities) > MaxCapabilities {
		return errors.New("worker capabilities are required and bounded")
	}
	for _, capability := range capabilities {
		if capability.Engine != speech.AVSpeechEngine && capability.Engine != speech.ChatterboxEngine {
			return ErrProtocol
		}
		if len(capability.Languages) == 0 || len(capability.Languages) > 32 || len(capability.UnitKinds) == 0 || len(capability.UnitKinds) > 8 || capability.MaxBytes < 0 || capability.MaxDurationMS < 0 {
			return ErrProtocol
		}
		for _, language := range capability.Languages {
			if language == "*" {
				continue
			}
			canonical, err := library.ParseBCP47(language)
			if err != nil || canonical != language {
				return ErrProtocol
			}
		}
		for _, unitKind := range capability.UnitKinds {
			if unitKind != "*" && unitKind != speech.UnitWord && unitKind != speech.UnitPhrase && unitKind != speech.UnitSentence {
				return ErrProtocol
			}
		}
	}
	return nil
}

func capabilitiesSubset(requested, enrolled []speech.WorkerCapability) bool {
	for _, want := range requested {
		covered := false
		for _, have := range enrolled {
			if have.Engine != want.Engine || !boundedCapacity(want.MaxBytes, have.MaxBytes) || !boundedCapacity(want.MaxDurationMS, have.MaxDurationMS) {
				continue
			}
			if !listSubset(want.Languages, have.Languages) || !listSubset(want.UnitKinds, have.UnitKinds) {
				continue
			}
			covered = true
			break
		}
		if !covered {
			return false
		}
	}
	return true
}

func boundedCapacity(requested, enrolled int64) bool {
	return enrolled == 0 || (requested > 0 && requested <= enrolled)
}

func listSubset(requested, enrolled []string) bool {
	for _, want := range requested {
		found := false
		for _, have := range enrolled {
			if have == "*" || have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validWorkerErrorCode(code string) bool {
	if len(code) == 0 || len(code) > 120 || !strings.HasPrefix(code, "v1.") {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func randomSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
