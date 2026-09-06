// Package localworker owns the transport DTOs, strict validation, and
// mapping rules for the ailocals.v1 universal worker protocol. It is a pure
// boundary package: no database, product, or HTTP-server imports.
package localworker

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"strings"
	"time"
)

// Shared wire constants (ailocals.v1 contract).
const (
	ProtocolVersion       = "ailocals.v1"
	RouteNamespace        = "api/ailocals/v1"
	WorkerTokenHeader     = "X-Ailocals-Worker-Token"
	EnrollmentTokenHeader = "X-Ailocals-Enrollment-Token"
	LeaseTokenHeader      = "X-Ailocals-Lease-Token"

	CapabilityACE         = "music.ace-step.v1"
	CapabilityAppleSpeech = "tts.apple-speech.v1"
	CapabilityChatterbox  = "tts.chatterbox.v3"
	CapabilityRelay       = "llm.openai-relay.v1"
	CategoryMusic         = "music"
	CategoryTTS           = "tts"
	CategoryLLM           = "llm"

	PollMaxSeconds         = 25
	LeaseSeconds           = 90
	HeartbeatSeconds       = 30
	PresenceSeconds        = 20
	ControlMaxBytes        = 262144
	PayloadMaxBytes        = 2097152
	ResultMaxBytes         = 2097152
	LeaseResponseMaxBytes  = 3 << 20
	ACEPayloadMaxBytes     = 65536
	MetadataPartMaxBytes   = 8192
	MultipartOverheadBytes = 65536

	EnrollmentLifetime     = 30 * time.Minute
	PresenceOfflineSeconds = 120

	MaxWorkerNameScalars  = 120
	MaxSoftwareVersionLen = 64
	MaxCapabilities       = 32
)

// Identifier, token, digest, and timestamp formats.
var (
	identifierRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	tokenRe         = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	sha256Re        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	timestampRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	serviceKindRe   = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	softwareRe      = regexp.MustCompile(`^[ -~]{1,64}$`)
	base64Canonical = regexp.MustCompile(`^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`)
)

// ErrorCode is an allowlisted protocol error code.
type ErrorCode string

// Common protocol error codes.
const (
	CodeUnauthorized          ErrorCode = "unauthorized"
	CodeEnrollmentInvalid     ErrorCode = "enrollment_invalid"
	CodeProtocolUnsupported   ErrorCode = "protocol_unsupported"
	CodeInvalidRequest        ErrorCode = "invalid_request"
	CodeUnsupportedCap        ErrorCode = "unsupported_capability"
	CodeWorkerBusy            ErrorCode = "worker_busy"
	CodeClientAlreadyEnrolled ErrorCode = "client_already_enrolled"
	CodeLeaseLost             ErrorCode = "lease_lost"
	CodeResultConflict        ErrorCode = "result_conflict"
	CodePayloadTooLarge       ErrorCode = "payload_too_large"
	CodeRateLimited           ErrorCode = "rate_limited"
	CodeInternalError         ErrorCode = "internal_error"
)

// HTTPStatus maps each error code to its fixed HTTP status.
func (c ErrorCode) HTTPStatus() int {
	switch c {
	case CodeUnauthorized, CodeEnrollmentInvalid:
		return 401
	case CodeWorkerBusy, CodeClientAlreadyEnrolled, CodeLeaseLost, CodeResultConflict:
		return 409
	case CodePayloadTooLarge:
		return 413
	case CodeRateLimited:
		return 429
	case CodeInternalError:
		return 503
	default:
		return 400
	}
}

// FailureCode is a common execution-failure outcome code (distinct from HTTP
// error codes).
type FailureCode string

// Common failure codes.
const (
	FailureCanceled          FailureCode = "canceled"
	FailureInterrupted       FailureCode = "interrupted"
	FailureResourceExhausted FailureCode = "resource_exhausted"
	FailureInvalidPayload    FailureCode = "invalid_payload"
	FailureSetupRequired     FailureCode = "setup_required"
	FailureExecutionFailed   FailureCode = "execution_failed"
	FailureRelayUnreachable  FailureCode = "relay_unreachable"
	FailureRelayAuth         FailureCode = "relay_auth"
	FailureRelayInvalidResp  FailureCode = "relay_invalid_response"
	FailureRelayModelUnknown FailureCode = "relay_model_unknown"
)

// Error is a protocol failure carrying its allowlisted code.
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "localworker error"
	}
	return fmt.Sprintf("localworker: %s: %s", e.Code, e.Message)
}

// NewError builds a bounded protocol error.
func NewError(code ErrorCode, message string) *Error {
	message = strings.TrimSpace(message)
	if len(message) > 256 {
		message = message[:256]
	}
	if message == "" {
		message = "request rejected"
	}
	return &Error{Code: code, Message: message}
}

// ErrorEnvelope builds the shared error response body.
func ErrorEnvelope(code ErrorCode, message string) map[string]any {
	return map[string]any{
		"protocol_version": ProtocolVersion,
		"error": map[string]any{
			"code":    string(code),
			"message": message,
		},
	}
}

// PresenceState is the advertised capability state.
type PresenceState string

// Presence states.
const (
	PresenceReady         PresenceState = "ready"
	PresenceBusy          PresenceState = "busy"
	PresencePaused        PresenceState = "paused"
	PresenceSetupRequired PresenceState = "setup_required"
	PresenceError         PresenceState = "error"
)

// PresenceEntry is one capability's full replacement state.
type PresenceEntry struct {
	ID         string        `json:"id"`
	State      PresenceState `json:"state"`
	Accepting  bool          `json:"accepting"`
	ActiveJobs int           `json:"active_jobs"`
	Reason     *string       `json:"reason"`
}

// AceCapabilityParameters is the strict ACE enrollment parameter object.
type AceCapabilityParameters struct {
	WorkerSchema        int      `json:"worker_schema"`
	ModelBundleRevision string   `json:"model_bundle_revision"`
	ManifestSHA256      string   `json:"manifest_sha256"`
	Accelerator         string   `json:"accelerator"`
	Formats             []string `json:"formats"`
}

// TtsCapabilityParameters nests the legacy speech capability unchanged.
type TtsCapabilityParameters struct {
	Engine        string   `json:"engine"`
	Languages     []string `json:"languages"`
	UnitKinds     []string `json:"unit_kinds"`
	MaxBytes      int64    `json:"max_bytes"`
	MaxDurationMS int64    `json:"max_duration_ms"`
}

// RelayCapabilityParameters is the strict relay parameter object.
type RelayCapabilityParameters struct {
	MaxCompletionBytes int64    `json:"max_completion_bytes"`
	Operations         []string `json:"operations"`
}

// CapabilityEntry is one enrollment capability with its typed parameters.
type CapabilityEntry struct {
	ID         string `json:"id"`
	Category   string `json:"category"`
	Parameters any    `json:"parameters"`
}

// EnrollRequest is the common enrollment body.
type EnrollRequest struct {
	ProtocolVersion string            `json:"protocol_version"`
	WorkerName      string            `json:"worker_name"`
	SoftwareVersion string            `json:"software_version"`
	Capabilities    []CapabilityEntry `json:"capabilities"`
}

// PresenceRequest is the full replacement presence snapshot.
type PresenceRequest struct {
	ProtocolVersion string          `json:"protocol_version"`
	Capabilities    []PresenceEntry `json:"capabilities"`
}

// LeaseRequest selects exactly one capability for one claim attempt window.
type LeaseRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	CapabilityID    string `json:"capability_id"`
	WaitSeconds     int    `json:"wait_seconds"`
}

// LeaseResponse is the common lease envelope.
type LeaseResponse struct {
	ProtocolVersion string  `json:"protocol_version"`
	JobID           string  `json:"job_id"`
	Attempt         int     `json:"attempt"`
	LeaseToken      string  `json:"lease_token"`
	LeaseExpiresAt  string  `json:"lease_expires_at"`
	DeadlineAt      *string `json:"deadline_at"`
	CapabilityID    string  `json:"capability_id"`
	PayloadEncoding string  `json:"payload_encoding"`
	PayloadBase64   string  `json:"payload_base64"`
	PayloadSHA256   string  `json:"payload_sha256"`
}

// HeartbeatRequest renews a lease.
type HeartbeatRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Attempt         int    `json:"attempt"`
	ProgressPercent int    `json:"progress_percent"`
}

// HeartbeatResponse renews or requests cancellation.
type HeartbeatResponse struct {
	ProtocolVersion string `json:"protocol_version"`
	LeaseExpiresAt  string `json:"lease_expires_at"`
	CancelRequested bool   `json:"cancel_requested"`
}

// FailRequest reports a terminal execution failure.
type FailRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Attempt         int    `json:"attempt"`
	Code            string `json:"code"`
	Retryable       bool   `json:"retryable"`
}

// CompleteMetadata is the metadata multipart part.
type CompleteMetadata struct {
	ProtocolVersion string `json:"protocol_version"`
	Attempt         int    `json:"attempt"`
	ResultSHA256    string `json:"result_sha256"`
}

// AcceptedResponse is the idempotent operation acknowledgement.
type AcceptedResponse struct {
	ProtocolVersion string `json:"protocol_version"`
	Accepted        bool   `json:"accepted"`
}

// DecodeStrictJSON decodes one JSON object with duplicate-key, unknown-field,
// trailing-data, and NaN rejection.
func DecodeStrictJSON(data []byte, target any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	return walkDup(decoder)
}

func walkDup(decoder *json.Decoder) error {
	tok, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyTok, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = true
			if err := walkDup(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token() // closing brace
		return err
	case '[':
		for decoder.More() {
			if err := walkDup(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token() // closing bracket
		return err
	default:
		return nil
	}
}

// SHA256Hex returns the lowercase hex digest of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashToken derives the durable hash for a credential.
func HashToken(token string) string {
	return SHA256Hex([]byte(token))
}

// ConstantTimeHashEquals compares a stored hash with the hash of raw.
func ConstantTimeHashEquals(storedHash, raw string) bool {
	expected := HashToken(raw)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(expected)) == 1
}

// NewSHA256 returns a fresh digest for incremental hashing.
func NewSHA256() hash.Hash { return sha256.New() }

// DecodePayload validates the lease envelope payload fields and returns the
// decoded domain payload bytes.
func DecodePayload(payloadBase64, payloadSHA256 string) ([]byte, error) {
	if !base64Canonical.MatchString(payloadBase64) {
		return nil, NewError(CodeInvalidRequest, "payload_base64 is not canonical base64")
	}
	if len(payloadBase64) > LeaseResponseMaxBytes {
		return nil, NewError(CodePayloadTooLarge, "encoded payload exceeds 3 MiB")
	}
	decoded, err := base64.StdEncoding.DecodeString(payloadBase64)
	if err != nil {
		return nil, NewError(CodeInvalidRequest, "payload_base64 is malformed")
	}
	if len(decoded) == 0 {
		return nil, NewError(CodeInvalidRequest, "payload must not be empty")
	}
	if len(decoded) > PayloadMaxBytes {
		return nil, NewError(CodePayloadTooLarge, "payload exceeds the byte bound")
	}
	if SHA256Hex(decoded) != payloadSHA256 {
		return nil, NewError(CodeInvalidRequest, "payload hash does not match")
	}
	return decoded, nil
}

// EncodePayload serializes exact domain bytes for the lease envelope.
func EncodePayload(payload []byte) (string, string, error) {
	if len(payload) == 0 {
		return "", "", NewError(CodeInvalidRequest, "payload must not be empty")
	}
	if len(payload) > PayloadMaxBytes {
		return "", "", NewError(CodePayloadTooLarge, "payload exceeds the byte bound")
	}
	return base64.StdEncoding.EncodeToString(payload), SHA256Hex(payload), nil
}

// ValidIdentifier reports whether value matches the shared identifier rule.
func ValidIdentifier(value string) bool {
	return identifierRe.MatchString(value)
}

// ValidToken reports whether value matches the 43-character token form.
func ValidToken(value string) bool {
	return tokenRe.MatchString(value)
}

// ValidSHA256 reports whether value is a lowercase 64-hex digest.
func ValidSHA256(value string) bool {
	return sha256Re.MatchString(value)
}

// ValidServiceKind reports whether value is a lowercase service identifier.
func ValidServiceKind(value string) bool {
	return serviceKindRe.MatchString(value)
}

// ParseTimestamp parses the strict millisecond UTC timestamp form.
func ParseTimestamp(value string) (time.Time, error) {
	if !timestampRe.MatchString(value) {
		return time.Time{}, NewError(CodeInvalidRequest, "timestamp must be RFC 3339 UTC with milliseconds")
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil {
		return time.Time{}, NewError(CodeInvalidRequest, "timestamp is invalid")
	}
	return parsed.UTC(), nil
}

// FormatTimestamp renders the strict millisecond UTC timestamp form.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// validateProtocolVersion rejects unsupported or missing protocol strings.
func validateProtocolVersion(value string) error {
	if value != ProtocolVersion {
		return NewError(CodeProtocolUnsupported, "unsupported protocol version")
	}
	return nil
}

// ValidateEnrollRequest enforces every enroll body rule.
func ValidateEnrollRequest(request *EnrollRequest) error {
	if request == nil {
		return NewError(CodeInvalidRequest, "enroll body is required")
	}
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if len(request.WorkerName) == 0 || len([]rune(request.WorkerName)) > MaxWorkerNameScalars {
		return NewError(CodeInvalidRequest, "worker_name length is out of bounds")
	}
	if !softwareRe.MatchString(request.SoftwareVersion) {
		return NewError(CodeInvalidRequest, "software_version format is invalid")
	}
	if len(request.Capabilities) < 1 || len(request.Capabilities) > MaxCapabilities {
		return NewError(CodeInvalidRequest, "capabilities must hold 1..32 entries")
	}
	seen := map[string]bool{}
	for index := range request.Capabilities {
		entry := &request.Capabilities[index]
		if err := validateCapabilityEntry(entry); err != nil {
			return err
		}
		if seen[entry.ID] {
			return NewError(CodeInvalidRequest, "capability entries must be unique")
		}
		seen[entry.ID] = true
	}
	return nil
}

func validateCapabilityEntry(entry *CapabilityEntry) error {
	if !validCapabilityID(entry.ID) {
		return NewError(CodeUnsupportedCap, "capability is not supported")
	}
	category, ok := capabilityCategory(entry.ID)
	if !ok || entry.Category != category {
		return NewError(CodeInvalidRequest, "capability category does not match")
	}
	switch entry.ID {
	case CapabilityACE:
		params, ok := entry.Parameters.(*AceCapabilityParameters)
		if !ok || params == nil {
			return NewError(CodeInvalidRequest, "capability parameters do not match")
		}
		return validateAceParameters(params)
	case CapabilityAppleSpeech, CapabilityChatterbox:
		params, ok := entry.Parameters.(*TtsCapabilityParameters)
		if !ok || params == nil {
			return NewError(CodeInvalidRequest, "capability parameters do not match")
		}
		return validateTtsParameters(entry.ID, params)
	case CapabilityRelay:
		params, ok := entry.Parameters.(*RelayCapabilityParameters)
		if !ok || params == nil {
			return NewError(CodeInvalidRequest, "capability parameters do not match")
		}
		return validateRelayParameters(params)
	}
	return NewError(CodeUnsupportedCap, "capability is not supported")
}

func validCapabilityID(id string) bool {
	switch id {
	case CapabilityACE, CapabilityAppleSpeech, CapabilityChatterbox, CapabilityRelay:
		return true
	}
	return false
}

func capabilityCategory(id string) (string, bool) {
	switch id {
	case CapabilityACE:
		return CategoryMusic, true
	case CapabilityAppleSpeech, CapabilityChatterbox:
		return CategoryTTS, true
	case CapabilityRelay:
		return CategoryLLM, true
	}
	return "", false
}

func validateAceParameters(params *AceCapabilityParameters) error {
	if params.WorkerSchema != 2 {
		return NewError(CodeInvalidRequest, "worker_schema must be 2")
	}
	if params.ModelBundleRevision == "" || len(params.ModelBundleRevision) > 128 {
		return NewError(CodeInvalidRequest, "model_bundle_revision length is out of bounds")
	}
	if !ValidSHA256(params.ManifestSHA256) {
		return NewError(CodeInvalidRequest, "manifest_sha256 format is invalid")
	}
	if params.Accelerator != "mps" {
		return NewError(CodeInvalidRequest, "accelerator must be mps")
	}
	if len(params.Formats) < 1 || len(params.Formats) > 3 {
		return NewError(CodeInvalidRequest, "formats must hold 1..3 entries")
	}
	for _, format := range params.Formats {
		if format != "mp3" && format != "flac" && format != "wav" {
			return NewError(CodeInvalidRequest, "formats entries are invalid")
		}
	}
	return nil
}

func validateTtsParameters(capabilityID string, params *TtsCapabilityParameters) error {
	expectedEngine := "avspeech"
	if capabilityID == CapabilityChatterbox {
		expectedEngine = "chatterbox"
	}
	if params.Engine != expectedEngine {
		return NewError(CodeInvalidRequest, "capability parameters do not match")
	}
	if len(params.Languages) < 1 || len(params.Languages) > 32 {
		return NewError(CodeInvalidRequest, "languages must hold 1..32 entries")
	}
	for _, language := range params.Languages {
		if language == "" {
			return NewError(CodeInvalidRequest, "languages entries must be strings")
		}
	}
	if len(params.UnitKinds) < 1 || len(params.UnitKinds) > 8 {
		return NewError(CodeInvalidRequest, "unit_kinds must hold 1..8 entries")
	}
	for _, kind := range params.UnitKinds {
		switch kind {
		case "word", "phrase", "sentence", "*":
		default:
			return NewError(CodeInvalidRequest, "unit_kinds entries are invalid")
		}
	}
	if params.MaxBytes < 0 || params.MaxDurationMS < 0 {
		return NewError(CodeInvalidRequest, "capability limits must be nonnegative")
	}
	return nil
}

func validateRelayParameters(params *RelayCapabilityParameters) error {
	if params.MaxCompletionBytes < 1 || params.MaxCompletionBytes > PayloadMaxBytes {
		return NewError(CodeInvalidRequest, "max_completion_bytes is out of range")
	}
	if len(params.Operations) < 1 || len(params.Operations) > 2 {
		return NewError(CodeInvalidRequest, "operations must hold 1..2 entries")
	}
	seen := map[string]bool{}
	for _, operation := range params.Operations {
		if operation != "chat_completion" && operation != "list_models" {
			return NewError(CodeInvalidRequest, "operations entries are invalid")
		}
		if seen[operation] {
			return NewError(CodeInvalidRequest, "operations entries must be unique")
		}
		seen[operation] = true
	}
	return nil
}

// ValidatePresenceRequest enforces the presence snapshot rules.
func ValidatePresenceRequest(request *PresenceRequest) error {
	if request == nil {
		return NewError(CodeInvalidRequest, "presence body is required")
	}
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if len(request.Capabilities) > MaxCapabilities {
		return NewError(CodeInvalidRequest, "capabilities must hold at most 32 entries")
	}
	seen := map[string]bool{}
	for index := range request.Capabilities {
		entry := &request.Capabilities[index]
		if !validCapabilityID(entry.ID) {
			return NewError(CodeUnsupportedCap, "capability is not supported")
		}
		if seen[entry.ID] {
			return NewError(CodeInvalidRequest, "presence entries must be unique")
		}
		seen[entry.ID] = true
		if entry.ActiveJobs < 0 || entry.ActiveJobs > 1 {
			return NewError(CodeInvalidRequest, "active_jobs must be 0 or 1")
		}
		if err := validateReason(entry); err != nil {
			return err
		}
	}
	return nil
}

func validateReason(entry *PresenceEntry) error {
	reason := ""
	if entry.Reason != nil {
		reason = *entry.Reason
	}
	validReason := func(reason string) bool {
		switch reason {
		case "", "slot_busy", "memory_pressure", "insufficient_memory",
			"storage_unavailable", "local_service_unreachable", "setup_missing", "user_paused":
			return true
		}
		return false
	}
	if entry.Reason != nil && !validReason(reason) {
		return NewError(CodeInvalidRequest, "presence reason is invalid")
	}
	resourceReason := func(reason string) bool {
		switch reason {
		case "slot_busy", "memory_pressure", "insufficient_memory",
			"storage_unavailable", "local_service_unreachable":
			return true
		}
		return false
	}
	switch entry.State {
	case PresenceReady:
		if !entry.Accepting || entry.ActiveJobs != 0 || entry.Reason != nil {
			return NewError(CodeInvalidRequest, "ready state is inconsistent")
		}
	case PresenceBusy:
		if entry.ActiveJobs == 1 {
			if entry.Reason != nil {
				return NewError(CodeInvalidRequest, "busy running reason must be null")
			}
		} else {
			if entry.Accepting || !resourceReason(reason) {
				return NewError(CodeInvalidRequest, "busy waiting requires a resource reason")
			}
		}
	case PresencePaused, PresenceSetupRequired, PresenceError:
		if entry.Accepting {
			return NewError(CodeInvalidRequest, "state must not accept work")
		}
	default:
		return NewError(CodeInvalidRequest, "presence state is invalid")
	}
	return nil
}

// ValidateLeaseRequest enforces the lease request rules.
func ValidateLeaseRequest(request *LeaseRequest) error {
	if request == nil {
		return NewError(CodeInvalidRequest, "lease body is required")
	}
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if !validCapabilityID(request.CapabilityID) {
		return NewError(CodeUnsupportedCap, "capability is not supported")
	}
	if request.WaitSeconds < 0 || request.WaitSeconds > PollMaxSeconds {
		return NewError(CodeInvalidRequest, "wait_seconds is out of range")
	}
	return nil
}

// ValidateHeartbeatRequest enforces the heartbeat request rules.
func ValidateHeartbeatRequest(request *HeartbeatRequest) error {
	if request == nil {
		return NewError(CodeInvalidRequest, "heartbeat body is required")
	}
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if request.Attempt < 1 {
		return NewError(CodeInvalidRequest, "attempt must be positive")
	}
	if request.ProgressPercent < 0 || request.ProgressPercent > 100 {
		return NewError(CodeInvalidRequest, "progress_percent is out of range")
	}
	return nil
}

// ValidFailureCode reports whether code is in the common failure allowlist.
func ValidFailureCode(code string) bool {
	switch FailureCode(code) {
	case FailureCanceled, FailureInterrupted, FailureResourceExhausted,
		FailureInvalidPayload, FailureSetupRequired, FailureExecutionFailed,
		FailureRelayUnreachable, FailureRelayAuth, FailureRelayInvalidResp,
		FailureRelayModelUnknown:
		return true
	}
	return false
}

// ValidateFailRequest enforces the fail request rules.
func ValidateFailRequest(request *FailRequest) error {
	if request == nil {
		return NewError(CodeInvalidRequest, "fail body is required")
	}
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if request.Attempt < 1 {
		return NewError(CodeInvalidRequest, "attempt must be positive")
	}
	if !ValidFailureCode(request.Code) {
		return NewError(CodeInvalidRequest, "failure code is not allowlisted")
	}
	return nil
}

// ValidateCompleteMetadata enforces the completion metadata rules.
func ValidateCompleteMetadata(request *CompleteMetadata) error {
	if request == nil {
		return NewError(CodeInvalidRequest, "completion metadata is required")
	}
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if request.Attempt < 1 {
		return NewError(CodeInvalidRequest, "attempt must be positive")
	}
	if !ValidSHA256(request.ResultSHA256) {
		return NewError(CodeInvalidRequest, "result_sha256 format is invalid")
	}
	return nil
}

// UnmarshalJSON decodes capability parameters according to the capability id
// so each implementation keeps its own strict parameter object.
func (e *CapabilityEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID         string          `json:"id"`
		Category   string          `json:"category"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.ID, e.Category = raw.ID, raw.Category
	strictDecode := func(target any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw.Parameters))
		decoder.DisallowUnknownFields()
		return decoder.Decode(target)
	}
	switch raw.ID {
	case CapabilityACE:
		var params AceCapabilityParameters
		if err := strictDecode(&params); err != nil {
			return err
		}
		e.Parameters = &params
	case CapabilityAppleSpeech, CapabilityChatterbox:
		var params TtsCapabilityParameters
		if err := strictDecode(&params); err != nil {
			return err
		}
		e.Parameters = &params
	case CapabilityRelay:
		var params RelayCapabilityParameters
		if err := strictDecode(&params); err != nil {
			return err
		}
		e.Parameters = &params
	default:
		var params map[string]any
		_ = json.Unmarshal(raw.Parameters, &params)
		e.Parameters = params
	}
	return nil
}
