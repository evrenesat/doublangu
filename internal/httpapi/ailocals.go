package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/localworker"
	"doublangu/internal/workers"
)

// AilocalsHandler exposes the common api/ailocals/v1 worker routes. Common
// headers apply only at these routes; legacy routes keep their exact
// behavior. Bodies are bounded while streaming.
type AilocalsHandler struct {
	service     *workers.Service
	environment string
}

// NewAilocalsHandler builds the handler with the configured deployment
// environment (beta, production, or development).
func NewAilocalsHandler(service *workers.Service, environment string) *AilocalsHandler {
	if environment == "" {
		environment = "development"
	}
	return &AilocalsHandler{service: service, environment: environment}
}

func ailocalsLimits() map[string]any {
	return map[string]any{
		"poll_max_seconds":  localworker.PollMaxSeconds,
		"lease_seconds":     localworker.LeaseSeconds,
		"heartbeat_seconds": localworker.HeartbeatSeconds,
		"presence_seconds":  localworker.PresenceSeconds,
		"control_max_bytes": int64(localworker.ControlMaxBytes),
		"payload_max_bytes": int64(localworker.PayloadMaxBytes),
		"result_max_bytes":  int64(localworker.ResultMaxBytes),
	}
}

// ServeInfo is the only unauthenticated common route.
func (h *AilocalsHandler) ServeInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"protocol_version":       localworker.ProtocolVersion,
		"service_kind":           "doublangu",
		"environment":            h.environment,
		"supported_capabilities": []string{localworker.CapabilityAppleSpeech, localworker.CapabilityChatterbox, localworker.CapabilityRelay},
		"limits":                 ailocalsLimits(),
	})
}

func (h *AilocalsHandler) ServeEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	body, ok := readControlBody(w, r)
	if !ok {
		return
	}
	var request localworker.EnrollRequest
	if err := localworker.DecodeStrictJSON(body, &request); err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "enroll body is invalid"))
		return
	}
	outcome, err := h.service.EnrollAilocals(r.Context(), r.Header.Get(localworker.EnrollmentTokenHeader), &request)
	if err != nil {
		writeAilocalsError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"protocol_version": localworker.ProtocolVersion,
		"worker_id":        outcome.Worker.ID.String(),
		"worker_token":     outcome.Token,
		"environment":      h.environment,
	})
}

func (h *AilocalsHandler) ServePresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	worker, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	body, ok := readControlBody(w, r)
	if !ok {
		return
	}
	var request localworker.PresenceRequest
	if err := localworker.DecodeStrictJSON(body, &request); err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "presence body is invalid"))
		return
	}
	serverTime, err := h.service.PresenceAilocals(r.Context(), worker, &request)
	if err != nil {
		writeAilocalsError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"protocol_version": localworker.ProtocolVersion,
		"server_time":      serverTime,
	})
}

func (h *AilocalsHandler) ServeLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	worker, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	body, ok := readControlBody(w, r)
	if !ok {
		return
	}
	var request localworker.LeaseRequest
	if err := localworker.DecodeStrictJSON(body, &request); err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "lease body is invalid"))
		return
	}
	lease, err := h.service.LeaseAilocals(r.Context(), worker, &request)
	if err != nil {
		writeAilocalsError(w, err)
		return
	}
	if lease == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	WriteJSON(w, http.StatusOK, lease)
}

func (h *AilocalsHandler) ServeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	worker, jobID, ok := h.authenticateWithJob(w, r)
	if !ok {
		return
	}
	body, ok := readControlBody(w, r)
	if !ok {
		return
	}
	var request localworker.HeartbeatRequest
	if err := localworker.DecodeStrictJSON(body, &request); err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "heartbeat body is invalid"))
		return
	}
	response, err := h.service.HeartbeatAilocals(r.Context(), worker, jobID, r.Header.Get(localworker.LeaseTokenHeader), &request)
	if err != nil {
		writeAilocalsError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, response)
}

func (h *AilocalsHandler) ServeFail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	worker, jobID, ok := h.authenticateWithJob(w, r)
	if !ok {
		return
	}
	body, ok := readControlBody(w, r)
	if !ok {
		return
	}
	var request localworker.FailRequest
	if err := localworker.DecodeStrictJSON(body, &request); err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "fail body is invalid"))
		return
	}
	if err := h.service.FailAilocals(r.Context(), worker, jobID, r.Header.Get(localworker.LeaseTokenHeader), &request); err != nil {
		writeAilocalsError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, localworker.AcceptedResponse{ProtocolVersion: localworker.ProtocolVersion, Accepted: true})
}

// ServeComplete consumes exactly metadata+result parts, plus exactly one
// artifact part for TTS leases. ACE and relay leases forbid artifact parts.
func (h *AilocalsHandler) ServeComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "method not allowed"))
		return
	}
	worker, jobID, ok := h.authenticateWithJob(w, r)
	if !ok {
		return
	}
	leaseToken := r.Header.Get(localworker.LeaseTokenHeader)
	if strings.TrimSpace(leaseToken) == "" {
		writeAilocalsError(w, localworker.NewError(localworker.CodeUnauthorized, "lease credential header is required"))
		return
	}
	// Total body bound: metadata + result + the largest legacy artifact
	// bound + multipart overhead, enforced while streaming.
	totalLimit := int64(localworker.MetadataPartMaxBytes + localworker.ResultMaxBytes + (64<<20)+localworker.MultipartOverheadBytes)
	r.Body = http.MaxBytesReader(w, r.Body, totalLimit)
	boundary, err := multipartBoundary(r.Header.Get("Content-Type"))
	if err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "content type must be multipart"))
		return
	}
	metadataBytes, result, artifact, err := readMultipartParts(r, boundary)
	if err != nil {
		writeAilocalsError(w, err)
		return
	}
	var metadata localworker.CompleteMetadata
	if err := localworker.DecodeStrictJSON(metadataBytes, &metadata); err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "completion metadata is invalid"))
		return
	}
	if err := localworker.ValidateCompleteMetadata(&metadata); err != nil {
		writeAilocalsError(w, err)
		return
	}
	digest := sha256.Sum256(result)
	if hex.EncodeToString(digest[:]) != metadata.ResultSHA256 {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "result hash does not match"))
		return
	}
	if err := h.service.CompleteAilocals(r.Context(), worker, jobID, leaseToken, &metadata, artifact, result); err != nil {
		writeAilocalsError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, localworker.AcceptedResponse{ProtocolVersion: localworker.ProtocolVersion, Accepted: true})
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func (h *AilocalsHandler) authenticate(w http.ResponseWriter, r *http.Request) (*workers.Worker, bool) {
	worker, err := h.service.AuthenticateAilocals(r.Context(), strings.TrimSpace(r.Header.Get(localworker.WorkerTokenHeader)))
	if err != nil {
		writeAilocalsError(w, err)
		return nil, false
	}
	return worker, true
}

func (h *AilocalsHandler) authenticateWithJob(w http.ResponseWriter, r *http.Request) (*workers.Worker, library.ULID, bool) {
	worker, ok := h.authenticate(w, r)
	if !ok {
		return nil, library.ULID(""), false
	}
	jobID, err := library.ParseULID(r.PathValue("id"))
	if err != nil {
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "job id is invalid"))
		return nil, library.ULID(""), false
	}
	return worker, jobID, true
}

func readControlBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, localworker.ControlMaxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if isRequestBodyTooLarge(err) {
			writeAilocalsError(w, localworker.NewError(localworker.CodePayloadTooLarge, "body exceeds the byte bound"))
			return nil, false
		}
		writeAilocalsError(w, localworker.NewError(localworker.CodeInvalidRequest, "body could not be read"))
		return nil, false
	}
	return body, true
}

func isRequestBodyTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

func multipartBoundary(contentType string) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}
	if mediaType != "multipart/form-data" {
		return "", errors.New("not multipart")
	}
	boundary := params["boundary"]
	if strings.TrimSpace(boundary) == "" {
		return "", errors.New("missing boundary")
	}
	return boundary, nil
}

func writeAilocalsError(w http.ResponseWriter, err error) {
	var protocolErr *localworker.Error
	if errors.As(err, &protocolErr) {
		WriteJSON(w, protocolErr.Code.HTTPStatus(), localworker.ErrorEnvelope(protocolErr.Code, protocolErr.Message))
		return
	}
	if errors.Is(err, jobs.ErrLeaseLost) || errors.Is(err, jobs.ErrLeaseExpired) {
		WriteJSON(w, http.StatusConflict, localworker.ErrorEnvelope(localworker.CodeLeaseLost, "lease is no longer valid"))
		return
	}
	WriteJSON(w, http.StatusServiceUnavailable, localworker.ErrorEnvelope(localworker.CodeInternalError, "request could not be completed"))
}

// readMultipartParts streams the multipart body, rejecting duplicate and
// unknown part names and enforcing per-part bounds.
func readMultipartParts(r *http.Request, boundary string) (metadata, result, artifact []byte, err error) {
	reader := multipart.NewReader(r.Body, boundary)
	seen := map[string]bool{}
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			if isRequestBodyTooLarge(partErr) || r.ContentLength > 0 {
				return nil, nil, nil, localworker.NewError(localworker.CodePayloadTooLarge, "completion exceeds the byte bound")
			}
			return nil, nil, nil, localworker.NewError(localworker.CodeInvalidRequest, "multipart body is malformed")
		}
		name := part.FormName()
		if seen[name] {
			return nil, nil, nil, localworker.NewError(localworker.CodeInvalidRequest, "duplicate multipart part")
		}
		seen[name] = true
		switch name {
		case localworker.MetadataPartName:
			if metadata, err = readBoundedPart(part, localworker.MetadataPartMaxBytes); err != nil {
				return nil, nil, nil, err
			}
		case localworker.ResultPartName:
			if result, err = readBoundedPart(part, localworker.ResultMaxBytes); err != nil {
				return nil, nil, nil, err
			}
		case localworker.ArtifactPartName:
			if artifact, err = readBoundedPart(part, 64<<20); err != nil {
				return nil, nil, nil, err
			}
		default:
			return nil, nil, nil, localworker.NewError(localworker.CodeInvalidRequest, "unknown multipart part")
		}
		part.Close()
	}
	if metadata == nil || result == nil {
		return nil, nil, nil, localworker.NewError(localworker.CodeInvalidRequest, "metadata and result parts are required")
	}
	return metadata, result, artifact, nil
}

func readBoundedPart(part *multipart.Part, limit int64) ([]byte, error) {
	limited := io.LimitReader(part, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, localworker.NewError(localworker.CodeInvalidRequest, "multipart part could not be read")
	}
	if int64(len(data)) > limit {
		return nil, localworker.NewError(localworker.CodePayloadTooLarge, "multipart part exceeds the bound")
	}
	return data, nil
}
