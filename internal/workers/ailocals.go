// Common ailocals.v1 facade over the existing worker service. Legacy
// Enroll/Lease/Heartbeat/Complete/Fail methods keep their exact inputs and
// response shapes; the facade adds transport DTO validation, the one
// common-client enrollment rule, presence snapshots, capability-exclusive
// leasing, and transaction-aware result digests.
package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/llmrelay"
	"doublangu/internal/localworker"
	"doublangu/internal/speech"
	"doublangu/internal/store"
)

// AilocalsEnrollOutcome carries the created worker and its one-time
// credential. The credential is shown only in the enrollment response; the
// configured environment is reported by the HTTP layer.
type AilocalsEnrollOutcome struct {
	Worker *Worker
	Token  string
}

const ailocalsProtocolMarker = `"protocol":"` + localworker.ProtocolVersion + `"`

// ailocalsMarkerProtocol returns the marker protocol string for presence
// validation in this package.
func ailocalsMarkerProtocol() string { return localworker.ProtocolVersion }

// AilocalsPresence is the durable presence snapshot shape.
type AilocalsPresence struct {
	Protocol              string                      `json:"protocol"`
	EnrolledCapabilityIDs []string                    `json:"enrolled_capability_ids"`
	ServerTime            string                      `json:"server_time"`
	Capabilities          []localworker.PresenceEntry `json:"capabilities"`
}

// EnrollAilocals consumes one owner-issued enrollment token and creates the
// single non-revoked common-client enrollment. The owner-selected capability
// subset is mapped into the existing capability columns and persisted as the
// grant; discovery and server demand never widen it.
func (s *Service) EnrollAilocals(ctx context.Context, token string, req *localworker.EnrollRequest) (*AilocalsEnrollOutcome, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workers: nil database")
	}
	if req == nil {
		return nil, localworker.NewError(localworker.CodeInvalidRequest, "enroll body is required")
	}
	if strings.TrimSpace(token) == "" {
		return nil, localworker.NewError(localworker.CodeUnauthorized, "enrollment token is required")
	}
	if err := localworker.ValidateEnrollRequest(req); err != nil {
		return nil, err
	}
	speechCaps := []speech.WorkerCapability{}
	relayCaps := []llmrelay.RelayCapability{}
	ids := []string{}
	for index := range req.Capabilities {
		entry := &req.Capabilities[index]
		switch entry.ID {
		case localworker.CapabilityAppleSpeech, localworker.CapabilityChatterbox:
			params, ok := entry.Parameters.(*localworker.TtsCapabilityParameters)
			if !ok || params == nil {
				return nil, localworker.NewError(localworker.CodeInvalidRequest, "capability parameters do not match")
			}
			speechCaps = append(speechCaps, speech.WorkerCapability{
				Engine: params.Engine, Languages: params.Languages,
				UnitKinds: params.UnitKinds, MaxBytes: params.MaxBytes,
				MaxDurationMS: params.MaxDurationMS,
			})
		case localworker.CapabilityRelay:
			params, ok := entry.Parameters.(*localworker.RelayCapabilityParameters)
			if !ok || params == nil {
				return nil, localworker.NewError(localworker.CodeInvalidRequest, "capability parameters do not match")
			}
			if params.MaxCompletionBytes != llmrelay.RelayCapabilityBytes {
				return nil, localworker.NewError(localworker.CodeInvalidRequest, "max_completion_bytes is not supported")
			}
			relayCaps = append(relayCaps, llmrelay.RelayCapability{MaxCompletionBytes: params.MaxCompletionBytes})
		default:
			return nil, localworker.NewError(localworker.CodeUnsupportedCap, "capability is not supported")
		}
		ids = append(ids, entry.ID)
	}
	grant := AilocalsPresence{
		Protocol:              localworker.ProtocolVersion,
		EnrolledCapabilityIDs: ids,
		ServerTime:            "",
		Capabilities:          []localworker.PresenceEntry{},
	}
	grantJSON, err := json.Marshal(grant)
	if err != nil {
		return nil, fmt.Errorf("workers: marshal grant: %w", err)
	}
	capabilitiesJSON, err := json.Marshal(speechCaps)
	if err != nil {
		return nil, fmt.Errorf("workers: marshal capabilities: %w", err)
	}
	relayJSON := []byte("[]")
	if len(relayCaps) > 0 {
		relayJSON, err = json.Marshal(relayCaps)
		if err != nil {
			return nil, fmt.Errorf("workers: marshal relay capabilities: %w", err)
		}
	}
	workerToken, err := randomSecret()
	if err != nil {
		return nil, err
	}
	now := store.NowUTC()
	var worker Worker
	err = s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var active int
		if scanErr := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM speech_worker WHERE revoked_at = '' AND ailocals_presence_json LIKE ?`,
			`%`+ailocalsProtocolMarker+`%`).Scan(&active); scanErr != nil {
			return scanErr
		}
		if active > 0 {
			return localworker.NewError(localworker.CodeClientAlreadyEnrolled,
				"a universal worker is already enrolled; revoke it before re-enrolling")
		}
		enrollmentID, expiresAt, found := "", "", false
		rows, scanErr := tx.QueryContext(ctx,
			`SELECT id, expires_at, token_hash FROM speech_worker_enrollment WHERE used_at = '' AND expires_at > ?`,
			store.NowUTC())
		if scanErr != nil {
			return scanErr
		}
		defer rows.Close()
		for rows.Next() {
			var candidateID, candidateExpires, hash string
			if scanErr := rows.Scan(&candidateID, &candidateExpires, &hash); scanErr != nil {
				return scanErr
			}
			if jobs.LeaseTokenMatches(token, hash) {
				enrollmentID, expiresAt, found = candidateID, candidateExpires, true
				break
			}
		}
		if scanErr := rows.Err(); scanErr != nil {
			return scanErr
		}
		if !found || expiresAt <= store.NowUTC() {
			return localworker.NewError(localworker.CodeEnrollmentInvalid, "enrollment token is invalid or expired")
		}
		worker = Worker{
			ID:                   library.NewULID(),
			Name:                 strings.TrimSpace(req.WorkerName),
			ProtocolVersion:      speech.ProtocolVersion,
			Capabilities:         speechCaps,
			SoftwareVersion:      strings.TrimSpace(req.SoftwareVersion),
			CreatedAt:            now,
			UpdatedAt:            now,
			LLMRelayCapabilities: relayCaps,
		}
		if _, execErr := tx.ExecContext(ctx, `INSERT INTO speech_worker
			(id, name, protocol_version, token_hash, capabilities_json, software_version, llm_relay_capabilities_json, ailocals_presence_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			worker.ID.String(), worker.Name, worker.ProtocolVersion, hashSecret(workerToken),
			string(capabilitiesJSON), worker.SoftwareVersion, string(relayJSON), string(grantJSON),
			now, now); execErr != nil {
			return execErr
		}
		_, execErr := tx.ExecContext(ctx,
			`UPDATE speech_worker_enrollment SET used_at = ? WHERE id = ? AND used_at = ''`,
			store.NowUTC(), enrollmentID)
		return execErr
	})
	if err != nil {
		return nil, err
	}
	return &AilocalsEnrollOutcome{Worker: &worker, Token: workerToken}, nil
}

// AuthenticateAilocals returns the active common worker for a credential.
// Legacy speech-worker credentials are rejected on common routes.
func (s *Service) AuthenticateAilocals(ctx context.Context, token string) (*Worker, error) {
	worker, err := s.Authenticate(ctx, token)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return nil, localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
		}
		return nil, err
	}
	presence, err := s.ailocalsPresence(ctx, worker.ID)
	if err != nil {
		return nil, err
	}
	if presence == nil || presence.Protocol != localworker.ProtocolVersion {
		return nil, localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	return worker, nil
}

func (s *Service) ailocalsPresence(ctx context.Context, id library.ULID) (*AilocalsPresence, error) {
	var raw string
	err := s.db.QueryRow(ctx, `SELECT ailocals_presence_json FROM speech_worker WHERE id = ?`, id.String()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	if err != nil {
		return nil, err
	}
	var presence AilocalsPresence
	if err := json.Unmarshal([]byte(raw), &presence); err != nil {
		return nil, nil
	}
	return &presence, nil
}

// PresenceAilocals records the full replacement capability snapshot, the
// worker last-seen time, and the common relay availability.
func (s *Service) PresenceAilocals(ctx context.Context, worker *Worker, req *localworker.PresenceRequest) (string, error) {
	if worker == nil || worker.RevokedAt != "" {
		return "", localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	if req == nil {
		return "", localworker.NewError(localworker.CodeInvalidRequest, "presence body is required")
	}
	if err := localworker.ValidatePresenceRequest(req); err != nil {
		return "", err
	}
	presence, err := s.ailocalsPresence(ctx, worker.ID)
	if err != nil {
		return "", err
	}
	if presence == nil {
		return "", localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	enrolled := map[string]bool{}
	for _, id := range presence.EnrolledCapabilityIDs {
		enrolled[id] = true
	}
	for index := range req.Capabilities {
		if !enrolled[req.Capabilities[index].ID] {
			return "", localworker.NewError(localworker.CodeUnsupportedCap,
				"presence advertises a capability outside the enrollment")
		}
	}
	now := store.NowUTC()
	snapshot := AilocalsPresence{
		Protocol:              localworker.ProtocolVersion,
		EnrolledCapabilityIDs: presence.EnrolledCapabilityIDs,
		ServerTime:            now,
		Capabilities:          req.Capabilities,
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("workers: marshal presence: %w", err)
	}
	relaySeen := ""
	for index := range req.Capabilities {
		entry := &req.Capabilities[index]
		if entry.ID == localworker.CapabilityRelay && (entry.State == "ready" || entry.State == "busy") {
			relaySeen = now
		}
	}
	result, err := s.db.Exec(ctx,
		`UPDATE speech_worker SET ailocals_presence_json = ?, last_seen_at = ?, relay_last_seen_at = ?, updated_at = ?
		 WHERE id = ? AND revoked_at = ''`,
		string(snapshotJSON), now, relaySeen, now, worker.ID.String())
	if err != nil {
		return "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return "", localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	return now, nil
}

// LeaseAilocals claims at most one job for exactly one capability. No work
// returns (nil, nil); a second active lease for the same capability is
// worker_busy. Relay leases carry the exact stored payload bytes; TTS leases
// carry the existing speech lease domain fields.
func (s *Service) LeaseAilocals(ctx context.Context, worker *Worker, req *localworker.LeaseRequest) (*localworker.LeaseResponse, error) {
	if worker == nil || worker.RevokedAt != "" {
		return nil, localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	if req == nil {
		return nil, localworker.NewError(localworker.CodeInvalidRequest, "lease body is required")
	}
	if err := localworker.ValidateLeaseRequest(req); err != nil {
		return nil, err
	}
	presence, err := s.ailocalsPresence(ctx, worker.ID)
	if err != nil {
		return nil, err
	}
	if presence == nil {
		return nil, localworker.NewError(localworker.CodeUnauthorized, "worker credential is not valid")
	}
	enrolled := map[string]bool{}
	for _, id := range presence.EnrolledCapabilityIDs {
		enrolled[id] = true
	}
	if !enrolled[req.CapabilityID] {
		return nil, localworker.NewError(localworker.CodeUnsupportedCap,
			"capability is outside this connection's enrollment")
	}
	jobType, err := localworker.JobTypeForCapability(req.CapabilityID)
	if err != nil {
		return nil, err
	}
	if active := s.ailocalsActiveLeases(ctx, worker.ID.String(), jobType); active > 0 {
		return nil, localworker.NewError(localworker.CodeWorkerBusy,
			"a second active lease for the same capability is not allowed")
	}
	if _, err := s.jobs.RecoverExpired(ctx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Duration(req.WaitSeconds) * time.Second)
	for {
		lease, err := s.jobs.ClaimMatchingCapability(ctx, jobs.TargetMacOS, worker.ID.String(), jobType)
		if err == nil {
			response, responseErr := s.ailocalsLeaseResponse(ctx, req.CapabilityID, lease)
			if responseErr != nil {
				s.rejectMalformedLease(ctx, lease)
				return nil, responseErr
			}
			return response, nil
		}
		if !errors.Is(err, jobs.ErrNoWork) {
			return nil, err
		}
		if req.WaitSeconds == 0 || time.Now().After(deadline) {
			return nil, nil
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

func (s *Service) ailocalsActiveLeases(ctx context.Context, ownerID, jobType string) int {
	var active int
	_ = s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM job WHERE lease_owner = ? AND job_type = ? AND state IN ('leased', 'running')`,
		ownerID, jobType).Scan(&active)
	return active
}

// ailocalsLeaseResponse validates the claimed job through the existing
// domain paths and builds the common lease envelope.
func (s *Service) ailocalsLeaseResponse(ctx context.Context, capabilityID string, lease *jobs.Lease) (*localworker.LeaseResponse, error) {
	if lease.JobType == jobs.LLMRelayJobType {
		// Exact stored payload bytes: the relay domain hash is computed
		// over them and is never recomputed from a reserialization.
		if _, err := localworker.DecodeRelayPayload([]byte(lease.PayloadJSON)); err != nil {
			return nil, ErrMalformedJob
		}
		return s.newAilocalsLeaseResponse(capabilityID, lease, []byte(lease.PayloadJSON))
	}
	legacy, err := s.leaseResponse(ctx, lease)
	if err != nil {
		return nil, err
	}
	payloadBytes, err := localworker.TtsPayloadFromLeaseDomain(
		legacy.RenderID.String(), legacy.RequestHash, legacy.SpeechUnitID.String(),
		legacy.Language, legacy.UnitKind, legacy.SpokenText, legacy.ContextPronunciationKey,
		legacy.Profile, legacy.Limits.MaxBytes, legacy.Limits.MaxDurationMS,
	)
	if err != nil {
		return nil, err
	}
	return s.newAilocalsLeaseResponse(capabilityID, lease, payloadBytes)
}

func (s *Service) newAilocalsLeaseResponse(capabilityID string, lease *jobs.Lease, payload []byte) (*localworker.LeaseResponse, error) {
	payloadBase64, payloadSHA256, err := localworker.EncodePayload(payload)
	if err != nil {
		return nil, err
	}
	return &localworker.LeaseResponse{
		ProtocolVersion: localworker.ProtocolVersion,
		JobID:           lease.ID.String(),
		Attempt:         lease.AttemptCount,
		LeaseToken:      lease.LeaseToken,
		LeaseExpiresAt:  lease.LeaseExpiresAt,
		DeadlineAt:      nil,
		CapabilityID:    capabilityID,
		PayloadEncoding: "base64",
		PayloadBase64:   payloadBase64,
		PayloadSHA256:   payloadSHA256,
	}, nil
}

// HeartbeatAilocals renews the common lease for the current attempt.
func (s *Service) HeartbeatAilocals(ctx context.Context, worker *Worker, jobID library.ULID, token string, req *localworker.HeartbeatRequest) (*localworker.HeartbeatResponse, error) {
	if req == nil {
		return nil, localworker.NewError(localworker.CodeInvalidRequest, "heartbeat body is required")
	}
	if err := localworker.ValidateHeartbeatRequest(req); err != nil {
		return nil, err
	}
	result, err := s.Heartbeat(ctx, worker, jobID, token, HeartbeatInput{
		ProtocolVersion: speech.ProtocolVersion,
		Attempt:         req.Attempt,
		ProgressPercent: req.ProgressPercent,
	})
	if err != nil {
		return nil, mapJobsError(err)
	}
	return &localworker.HeartbeatResponse{
		ProtocolVersion: localworker.ProtocolVersion,
		LeaseExpiresAt:  result.LeaseExpiresAt,
		CancelRequested: result.CancelRequested,
	}, nil
}

// FailAilocals records a terminal failure with the product's allowlisted
// code for the workload. Unknown mappings fail validation.
func (s *Service) FailAilocals(ctx context.Context, worker *Worker, jobID library.ULID, token string, req *localworker.FailRequest) error {
	if req == nil {
		return localworker.NewError(localworker.CodeInvalidRequest, "fail body is required")
	}
	if err := localworker.ValidateFailRequest(req); err != nil {
		return err
	}
	job, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		return err
	}
	mapped, err := localworker.MapFailureToProductCode(req.Code, job.JobType == jobs.LLMRelayJobType)
	if err != nil {
		return err
	}
	if err := s.Fail(ctx, worker, jobID, token, FailInput{
		ProtocolVersion: speech.ProtocolVersion,
		Attempt:         req.Attempt,
		ErrorCode:       mapped,
		Retry:           req.Retryable,
	}); err != nil {
		return mapJobsError(err)
	}
	return nil
}

// CompleteAilocals validates the common completion parts and commits
// through the existing publication paths. The common result digest is
// written in the same transaction as success; repeated identical
// completions return success after lease loss.
func (s *Service) CompleteAilocals(ctx context.Context, worker *Worker, jobID library.ULID, leaseToken string, metadata *localworker.CompleteMetadata, artifact, result []byte) error {
	if metadata == nil {
		return localworker.NewError(localworker.CodeInvalidRequest, "completion metadata is required")
	}
	if err := localworker.ValidateCompleteMetadata(metadata); err != nil {
		return err
	}
	job, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		return err
	}
	isRelay := job.JobType == jobs.LLMRelayJobType
	digest := metadata.ResultSHA256
	// Identical accepted completion retry: verify the stored digest plus the
	// existing artifact/result identity before returning success.
	if job.State == jobs.StateSucceeded {
		return s.verifyAilocalsRepeatedCompletion(ctx, job, digest, artifact, result)
	}
	if job.State != jobs.StateLeased && job.State != jobs.StateRunning {
		return localworker.NewError(localworker.CodeLeaseLost, "lease is no longer valid")
	}
	if _, err := s.jobs.VerifyLease(ctx, jobID, metadata.Attempt, leaseToken, worker.ID.String()); err != nil {
		return mapJobsError(err)
	}
	if isRelay {
		if artifact != nil {
			return localworker.NewError(localworker.CodeInvalidRequest, "artifact parts are not accepted")
		}
		if err := s.completeRelayCommon(ctx, job, metadata.Attempt, leaseToken, result, digest); err != nil {
			return err
		}
		return nil
	}
	completionMetadata, err := decodeAilocalsTtsResult(result)
	if err != nil {
		return err
	}
	completionMetadata.Attempt = metadata.Attempt
	if err := s.completeSpeechCommon(ctx, job, completionMetadata, artifact, digest, leaseToken); err != nil {
		return err
	}
	return nil
}

// verifyAilocalsRepeatedCompletion implements the common idempotency rules
// on an already-succeeded job: TTS compares the stored digest and the
// durable artifact identity; relay compares the exact result bytes through
// the existing relay result record.
func (s *Service) verifyAilocalsRepeatedCompletion(ctx context.Context, job *jobs.Job, digest string, artifact, result []byte) error {
	if job.AilocalsResultSHA256 != digest {
		return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
	}
	if job.JobType == jobs.LLMRelayJobType {
		if len(result) == 0 {
			return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
		}
		// The stored result hash is over the canonical encoding; canonicalize
		// the retried bytes the same way before comparing.
		envelope, envelopeErr := localworker.DecodeRelayPayload([]byte(job.PayloadJSON))
		if envelopeErr != nil {
			return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
		}
		var canonical []byte
		var canonicalErr error
		if envelope.Operation == llmrelay.OperationChatCompletion {
			decoded, decodeErr := llmrelay.DecodeChatResult(result, envelope.RequestID, localworker.ResultMaxBytes)
			if decodeErr != nil {
				return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
			}
			canonical, canonicalErr = llmrelay.CanonicalChatResult(decoded)
		} else {
			decoded, decodeErr := llmrelay.DecodeListModelsResult(result, envelope.RequestID, localworker.ResultMaxBytes)
			if decodeErr != nil {
				return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
			}
			canonical, canonicalErr = llmrelay.CanonicalListModelsResult(decoded)
		}
		if canonicalErr != nil {
			return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
		}
		var storedHash string
		err := s.db.QueryRow(ctx, `SELECT result_hash FROM llm_relay_result WHERE job_id = ?`, job.ID.String()).Scan(&storedHash)
		if err != nil || storedHash != localworker.SHA256Hex(canonical) {
			return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
		}
		return nil
	}
	repeated, err := decodeAilocalsTtsResult(result)
	if err != nil || repeated == nil || repeated.Artifact == nil {
		return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
	}
	// The retried artifact bytes must hash to their own metadata identity.
	if len(artifact) != int(repeated.Artifact.SizeBytes) || localworker.SHA256Hex(artifact) != repeated.Artifact.SHA256 {
		return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
	}
	var storedDigest string
	var storedSize int64
	err = s.db.QueryRow(ctx,
		`SELECT COALESCE((SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = audio_render.id), ''), size_bytes
		 FROM audio_render WHERE request_hash = ?`, job.InputHash).Scan(&storedDigest, &storedSize)
	if err != nil || storedDigest != repeated.Artifact.SHA256 || storedSize != repeated.Artifact.SizeBytes {
		return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
	}
	return nil
}

// decodeAilocalsTtsResult decodes the strict common TTS result part:
// {"artifact": <existing ArtifactMetadata>}.
func decodeAilocalsTtsResult(result []byte) (*CompleteMetadata, error) {
	if len(result) == 0 || len(result) > localworker.ResultMaxBytes {
		return nil, localworker.NewError(localworker.CodePayloadTooLarge, "result part exceeds the bound")
	}
	var parsed struct {
		Artifact *speech.ArtifactMetadata `json:"artifact"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(result)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, localworker.NewError(localworker.CodeInvalidRequest, "result part is not valid TTS metadata")
	}
	if parsed.Artifact == nil {
		return nil, localworker.NewError(localworker.CodeInvalidRequest, "result part is missing the artifact")
	}
	return &CompleteMetadata{Attempt: 0, Artifact: parsed.Artifact}, nil
}

// completeSpeechCommon commits TTS audio through the existing publication
// path with the common result digest in the same transaction.
func (s *Service) completeSpeechCommon(ctx context.Context, job *jobs.Job, metadata *CompleteMetadata, audio []byte, digest, leaseToken string) error {
	if metadata == nil || metadata.Artifact == nil {
		return localworker.NewError(localworker.CodeInvalidRequest, "result part is missing the artifact")
	}
	var payload speech.JobPayload
	if err := decodeStrict([]byte(job.PayloadJSON), &payload); err != nil {
		return localworker.NewError(localworker.CodeInvalidRequest, "job payload is malformed")
	}
	if metadata.Artifact.RequestHash != payload.RequestHash || job.InputHash != payload.RequestHash {
		return localworker.NewError(localworker.CodeInvalidRequest, "artifact does not match the leased request")
	}
	if int64(len(audio)) != metadata.Artifact.SizeBytes || len(audio) == 0 {
		return localworker.NewError(localworker.CodeInvalidRequest, "artifact does not match its metadata")
	}
	if err := speech.ValidateArtifact(*metadata.Artifact, int64(len(audio)), audio, payload.RequestHash, payload.UnitKind); err != nil {
		return localworker.NewError(localworker.CodeInvalidRequest, "artifact metadata is invalid")
	}
	renderID, err := library.ParseULID(payload.RenderID)
	if err != nil {
		return localworker.NewError(localworker.CodeInvalidRequest, "job payload is malformed")
	}
	if err := s.speech.SetRenderGenerating(ctx, renderID); err != nil {
		return err
	}
	prepared, err := s.media.PrepareWrite(audio)
	if err != nil {
		_ = s.speech.MarkRenderFailed(ctx, renderID, "v1.audio_upload_failed")
		_ = s.jobs.Fail(ctx, job.ID, metadata.Attempt, "", "v1.audio_upload_failed", true)
		return err
	}
	_, err = s.media.CommitPrepared(ctx, s.db, speech.AudioMIME, prepared, func(tx *sql.Tx, blobDigest string, _ int64) error {
		if err := speech.MarkRenderReadyTx(ctx, tx, renderID, payload.RequestHash, blobDigest, *metadata.Artifact); err != nil {
			return err
		}
		if err := jobs.CompleteTx(ctx, tx, job.ID, metadata.Attempt, leaseToken); err != nil {
			return err
		}
		return writeAilocalsDigestTx(ctx, tx, job.ID, digest)
	})
	if err != nil {
		_ = s.speech.MarkRenderFailed(ctx, renderID, "v1.audio_upload_failed")
		return localworker.NewError(localworker.CodeInvalidRequest, "completion could not be committed")
	}
	s.recomputeRenderArticles(ctx, renderID)
	return nil
}

// completeRelayCommon commits the exact relay result bytes through the
// existing atomic relay persistence with the common digest in the same
// transaction.
func (s *Service) completeRelayCommon(ctx context.Context, job *jobs.Job, attempt int, token string, result []byte, digest string) error {
	if len(result) == 0 || len(result) > localworker.ResultMaxBytes {
		return localworker.NewError(localworker.CodeInvalidRequest, "result part exceeds the bound")
	}
	err := s.db.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.relay.CompleteTx(ctx, tx, *job, attempt, token, result); err != nil {
			return err
		}
		return writeAilocalsDigestTx(ctx, tx, job.ID, digest)
	})
	if err == nil {
		return nil
	}
	var nondeterministic *llmrelay.NondeterministicError
	if errors.As(err, &nondeterministic) {
		_ = s.jobs.Fail(ctx, job.ID, attempt, token, llmrelay.CodeNondeterministic, false)
		return localworker.NewError(localworker.CodeResultConflict, "result conflicts with acceptance")
	}
	if errors.Is(err, jobs.ErrLeaseLost) || errors.Is(err, jobs.ErrLeaseExpired) {
		return mapJobsError(err)
	}
	return localworker.NewError(localworker.CodeInvalidRequest, "completion could not be committed")
}

func writeAilocalsDigestTx(ctx context.Context, tx *sql.Tx, jobID library.ULID, digest string) error {
	_, err := tx.ExecContext(ctx, `UPDATE job SET ailocals_result_sha256 = ? WHERE id = ?`, digest, jobID.String())
	if err != nil {
		return fmt.Errorf("workers: write ailocals digest: %w", err)
	}
	return nil
}

// mapJobsError converts durable job-store errors to common protocol errors.
func mapJobsError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, jobs.ErrLeaseLost) || errors.Is(err, jobs.ErrLeaseExpired) {
		return localworker.NewError(localworker.CodeLeaseLost, "lease is no longer valid")
	}
	return err
}
