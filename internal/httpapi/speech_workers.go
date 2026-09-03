package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/llmrelay"
	"doublangu/internal/speech"
	"doublangu/internal/workers"
)

// SpeechWorkerHandler exposes owner enrollment/status routes and the separate
// application-authenticated outbound worker protocol.
type SpeechWorkerHandler struct {
	service *workers.Service
	csrf    CSRFVerifier
}

func NewSpeechWorkerHandler(service *workers.Service, csrf CSRFVerifier) *SpeechWorkerHandler {
	return &SpeechWorkerHandler{service: service, csrf: csrf}
}

func (h *SpeechWorkerHandler) ServeOwnerEnrollments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
		WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
		return
	}
	enrollment, err := h.service.CreateEnrollment(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "worker enrollment unavailable", ErrCodeInternal)
		return
	}
	WriteJSON(w, http.StatusCreated, enrollment)
}

func (h *SpeechWorkerHandler) ServeOwnerWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	workersList, err := h.service.ListWorkers(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "worker status unavailable", ErrCodeInternal)
		return
	}
	WriteOK(w, workersList)
}

func (h *SpeechWorkerHandler) ServeOwnerWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	if h.csrf == nil || h.csrf.VerifyRequest(r) != nil {
		WriteError(w, http.StatusForbidden, "csrf token is missing or invalid", ErrCodeCSRF)
		return
	}
	id, err := parseWorkerID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid worker id", ErrCodeValidation)
		return
	}
	if err := h.service.Revoke(r.Context(), id); err != nil {
		WriteError(w, http.StatusInternalServerError, "worker revocation failed", ErrCodeInternal)
		return
	}
	WriteOK(w, map[string]bool{"ok": true})
}

func (h *SpeechWorkerHandler) authenticate(w http.ResponseWriter, r *http.Request) (*workers.Worker, bool) {
	worker, err := h.service.Authenticate(r.Context(), strings.TrimSpace(r.Header.Get("X-Doublangu-Worker-Token")))
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "worker authentication required", ErrCodeWorkerAuth)
		return nil, false
	}
	return worker, true
}

func (h *SpeechWorkerHandler) ServeEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	var input workers.EnrollInput
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid worker enrollment request", ErrCodeWorkerProtocol)
		return
	}
	worker, token, err := h.service.Enroll(r.Context(), r.Header.Get("X-Doublangu-Enrollment-Token"), input)
	if err != nil {
		if errors.Is(err, workers.ErrUnauthorized) {
			WriteError(w, http.StatusUnauthorized, "worker enrollment token is invalid or expired", ErrCodeWorkerAuth)
		} else if errors.Is(err, workers.ErrProtocol) {
			WriteError(w, http.StatusBadRequest, "worker protocol or capabilities are unsupported", ErrCodeWorkerProtocol)
		} else {
			WriteError(w, http.StatusInternalServerError, "worker enrollment failed", ErrCodeInternal)
		}
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"worker": worker, "worker_token": token, "protocol_version": speech.ProtocolVersion})
}

func (h *SpeechWorkerHandler) ServeLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	worker, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var input workers.LeaseRequest
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid worker lease request", ErrCodeWorkerProtocol)
		return
	}
	lease, err := h.service.Lease(r.Context(), worker, input)
	if err != nil {
		switch {
		case errors.Is(err, workers.ErrNoWork):
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, workers.ErrProtocol):
			WriteError(w, http.StatusBadRequest, "worker protocol or capabilities are unsupported", ErrCodeWorkerProtocol)
		case errors.Is(err, workers.ErrMalformedJob):
			WriteError(w, http.StatusInternalServerError, "speech queue contains an invalid job", ErrCodeInternal)
		default:
			WriteError(w, http.StatusInternalServerError, "worker lease failed", ErrCodeInternal)
		}
		return
	}
	WriteOK(w, lease)
}

func (h *SpeechWorkerHandler) ServeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	worker, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	jobID, err := parseWorkerID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid job id", ErrCodeValidation)
		return
	}
	var input workers.HeartbeatInput
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid heartbeat request", ErrCodeWorkerProtocol)
		return
	}
	result, err := h.service.Heartbeat(r.Context(), worker, jobID, r.Header.Get("X-Doublangu-Lease-Token"), input)
	if err != nil {
		writeWorkerOperationError(w, err, "heartbeat failed")
		return
	}
	WriteOK(w, result)
}

func (h *SpeechWorkerHandler) ServeFail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	worker, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	jobID, err := parseWorkerID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid job id", ErrCodeValidation)
		return
	}
	var input workers.FailInput
	if err := decodeJSONObject(w, r, &input); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid worker failure request", ErrCodeWorkerProtocol)
		return
	}
	if err := h.service.Fail(r.Context(), worker, jobID, r.Header.Get("X-Doublangu-Lease-Token"), input); err != nil {
		writeWorkerOperationError(w, err, "worker failure could not be recorded")
		return
	}
	WriteOK(w, map[string]bool{"ok": true})
}

func (h *SpeechWorkerHandler) ServeComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, http.StatusMethodNotAllowed, "method not allowed", ErrCodeMethodNotAllow)
		return
	}
	worker, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	jobID, err := parseWorkerID(r.PathValue("id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid job id", ErrCodeValidation)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65<<20)
	multipart, err := r.MultipartReader()
	if err != nil {
		WriteError(w, http.StatusBadRequest, "multipart audio upload is required", ErrCodeAudioUploadRejected)
		return
	}
	var metadata workers.CompleteMetadata
	var audio, result []byte
	seenMetadata, seenAudio, seenResult := false, false, false
	for {
		part, nextErr := multipart.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			WriteError(w, http.StatusBadRequest, "malformed multipart audio upload", ErrCodeAudioUploadRejected)
			return
		}
		name := part.FormName()
		switch name {
		case "metadata":
			if seenMetadata {
				WriteError(w, http.StatusBadRequest, "audio metadata was repeated", ErrCodeAudioUploadRejected)
				return
			}
			value, readErr := io.ReadAll(io.LimitReader(part, 1<<20))
			if readErr != nil || len(value) >= 1<<20 || decodeStrictJSON(value, &metadata) != nil {
				WriteError(w, http.StatusBadRequest, "invalid audio metadata", ErrCodeAudioUploadRejected)
				return
			}
			seenMetadata = true
		case "audio":
			if seenAudio {
				WriteError(w, http.StatusBadRequest, "audio file was repeated", ErrCodeAudioUploadRejected)
				return
			}
			value, readErr := io.ReadAll(io.LimitReader(part, 64<<20+1))
			if readErr != nil || len(value) > 64<<20 {
				WriteError(w, http.StatusRequestEntityTooLarge, "audio file is too large", ErrCodeAudioUploadRejected)
				return
			}
			audio = value
			seenAudio = true
		case "result":
			if seenResult {
				WriteError(w, http.StatusBadRequest, "relay result was repeated", ErrCodeRelayUploadRejected)
				return
			}
			value, readErr := io.ReadAll(io.LimitReader(part, (2<<20)+1))
			if readErr != nil || len(value) > 2<<20 {
				WriteError(w, http.StatusRequestEntityTooLarge, "relay result is too large", ErrCodeRelayUploadRejected)
				return
			}
			result = value
			seenResult = true
		default:
			WriteError(w, http.StatusBadRequest, "unknown multipart field", ErrCodeAudioUploadRejected)
			return
		}
	}
	// Metadata is the only HTTP-level requirement. Whether `audio` or
	// `result` is required is discriminated by the leased job type after the
	// service loads the job lease, so a missing payload part is judged by the
	// service: ErrUploadRejected for TTS shapes, ErrRelayRejected for relay
	// shapes.
	if !seenMetadata {
		WriteError(w, http.StatusBadRequest, "audio metadata is required", ErrCodeAudioUploadRejected)
		return
	}
	if err := h.service.Complete(r.Context(), worker, jobID, metadata, audio, result); err != nil {
		writeWorkerOperationError(w, err, "audio upload rejected")
		return
	}
	WriteOK(w, map[string]bool{"ok": true})
}

func writeWorkerOperationError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, workers.ErrUnauthorized):
		WriteError(w, http.StatusUnauthorized, "worker authentication required", ErrCodeWorkerAuth)
	case errors.Is(err, jobs.ErrLeaseExpired), errors.Is(err, jobs.ErrLeaseLost):
		WriteError(w, http.StatusConflict, "worker lease is no longer valid", ErrCodeWorkerOffline)
	case errors.Is(err, workers.ErrNondeterministic):
		WriteError(w, http.StatusConflict, "audio result differs from the accepted render", ErrCodeAudioNondeterministic)
	case errors.Is(err, workers.ErrUploadRejected):
		WriteError(w, http.StatusUnprocessableEntity, "audio upload failed validation", ErrCodeAudioUploadRejected)
	case errors.Is(err, workers.ErrRelayRejected):
		WriteError(w, http.StatusUnprocessableEntity, "relay upload failed validation", ErrCodeRelayUploadRejected)
	case errors.Is(err, workers.ErrMalformedJob):
		WriteError(w, http.StatusInternalServerError, "speech job is malformed", ErrCodeInternal)
	default:
		var nondeterministic *llmrelay.NondeterministicError
		if errors.As(err, &nondeterministic) {
			WriteError(w, http.StatusConflict, "relay result differs from the accepted result", ErrCodeRelayNondeterministic)
			return
		}
		WriteError(w, http.StatusInternalServerError, fallback, ErrCodeInternal)
	}
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func parseWorkerID(raw string) (library.ULID, error) {
	return library.ParseULID(strings.TrimSpace(raw))
}
