package localworker

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func aceEntry() CapabilityEntry {
	return CapabilityEntry{
		ID:       CapabilityACE,
		Category: CategoryMusic,
		Parameters: &AceCapabilityParameters{
			WorkerSchema:        2,
			ModelBundleRevision: "fixture-bundle-1",
			ManifestSHA256:      strings.Repeat("a", 64),
			Accelerator:         "mps",
			Formats:             []string{"mp3"},
		},
	}
}

func appleEntry() CapabilityEntry {
	return CapabilityEntry{
		ID:       CapabilityAppleSpeech,
		Category: CategoryTTS,
		Parameters: &TtsCapabilityParameters{
			Engine:        "avspeech",
			Languages:     []string{"nl-NL"},
			UnitKinds:     []string{"word", "phrase", "sentence"},
			MaxBytes:      2097152,
			MaxDurationMS: 120000,
		},
	}
}

func chatterboxEntry() CapabilityEntry {
	entry := appleEntry()
	entry.ID = CapabilityChatterbox
	entry.Parameters = &TtsCapabilityParameters{
		Engine:        "chatterbox",
		Languages:     []string{"nl"},
		UnitKinds:     []string{"sentence"},
		MaxBytes:      2097152,
		MaxDurationMS: 60000,
	}
	return entry
}

func relayEntry() CapabilityEntry {
	return CapabilityEntry{
		ID:       CapabilityRelay,
		Category: CategoryLLM,
		Parameters: &RelayCapabilityParameters{
			MaxCompletionBytes: PayloadMaxBytes,
			Operations:         []string{"chat_completion", "list_models"},
		},
	}
}

func TestErrorCodeHTTPStatus(t *testing.T) {
	cases := map[ErrorCode]int{
		CodeUnauthorized:          401,
		CodeEnrollmentInvalid:     401,
		CodeInvalidRequest:        400,
		CodeProtocolUnsupported:   400,
		CodeUnsupportedCap:        400,
		CodeWorkerBusy:            409,
		CodeClientAlreadyEnrolled: 409,
		CodeLeaseLost:             409,
		CodeResultConflict:        409,
		CodePayloadTooLarge:       413,
		CodeRateLimited:           429,
		CodeInternalError:         503,
	}
	for code, want := range cases {
		if got := code.HTTPStatus(); got != want {
			t.Fatalf("%s status = %d, want %d", code, got, want)
		}
	}
}

func TestDecodeStrictJSONRejectsDuplicatesTrailingAndUnknown(t *testing.T) {
	var body map[string]any
	if err := DecodeStrictJSON([]byte(`{"a": 1}`), &body); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	if err := DecodeStrictJSON([]byte(`{"a": 1, "a": 2}`), &body); err == nil {
		t.Fatal("duplicate keys must be rejected")
	}
	if err := DecodeStrictJSON([]byte(`{"a": 1} {"b": 2}`), &body); err == nil {
		t.Fatal("trailing JSON must be rejected")
	}
	type strict struct {
		A int `json:"a"`
	}
	var typed strict
	if err := DecodeStrictJSON([]byte(`{"a": 1, "b": 2}`), &typed); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	nested := []byte(`{"a": {"b": 1, "b": 2}}`)
	if err := DecodeStrictJSON(nested, &body); err == nil {
		t.Fatal("nested duplicate keys must be rejected")
	}
}

func TestTimestampRoundTripAndStrictness(t *testing.T) {
	value := "2026-09-05T12:00:00.123Z"
	parsed, err := ParseTimestamp(value)
	if err != nil {
		t.Fatalf("ParseTimestamp failed: %v", err)
	}
	if got := FormatTimestamp(parsed); got != value {
		t.Fatalf("FormatTimestamp = %q, want %q", got, value)
	}
	for _, bad := range []string{"2026-09-05T12:00:00Z", "2026-09-05T12:00:00.123+00:00", ""} {
		if _, err := ParseTimestamp(bad); err == nil {
			t.Fatalf("timestamp %q must be rejected", bad)
		}
	}
}

func TestValidateEnrollRequestAcceptsOwnerSelectedSubsets(t *testing.T) {
	three := EnrollRequest{
		ProtocolVersion: ProtocolVersion,
		WorkerName:      "Fixture Mac",
		SoftwareVersion: "0.1.0 (1)",
		Capabilities:    []CapabilityEntry{appleEntry(), chatterboxEntry(), relayEntry()},
	}
	if err := ValidateEnrollRequest(&three); err != nil {
		t.Fatalf("apple+chatterbox+relay subset rejected: %v", err)
	}
	aceOnly := EnrollRequest{
		ProtocolVersion: ProtocolVersion,
		WorkerName:      "Music Mac",
		SoftwareVersion: "0.1.0",
		Capabilities:    []CapabilityEntry{aceEntry()},
	}
	if err := ValidateEnrollRequest(&aceOnly); err != nil {
		t.Fatalf("ace-only subset rejected: %v", err)
	}
	appleOnly := aceOnly
	appleOnly.Capabilities = []CapabilityEntry{appleEntry()}
	if err := ValidateEnrollRequest(&appleOnly); err != nil {
		t.Fatalf("apple-only subset rejected: %v", err)
	}
}

func TestValidateEnrollRequestRejectsInvalidBodies(t *testing.T) {
	valid := EnrollRequest{
		ProtocolVersion: ProtocolVersion,
		WorkerName:      "Fixture Mac",
		SoftwareVersion: "0.1.0",
		Capabilities:    []CapabilityEntry{appleEntry(), chatterboxEntry()},
	}
	if err := ValidateEnrollRequest(&valid); err != nil {
		t.Fatalf("valid enroll rejected: %v", err)
	}
	cases := []func(*EnrollRequest){
		func(r *EnrollRequest) { r.ProtocolVersion = "speech-worker.v1" },
		func(r *EnrollRequest) { r.WorkerName = strings.Repeat("x", 121) },
		func(r *EnrollRequest) { r.WorkerName = "" },
		func(r *EnrollRequest) { r.SoftwareVersion = "bad\nversion" },
		func(r *EnrollRequest) { r.Capabilities = nil },
		func(r *EnrollRequest) { r.Capabilities = []CapabilityEntry{appleEntry(), appleEntry()} },
	}
	for index, mutate := range cases {
		candidate := valid
		mutate(&candidate)
		if err := ValidateEnrollRequest(&candidate); err == nil {
			t.Fatalf("case %d must be rejected", index)
		}
	}
	engineMismatch := valid
	bad := appleEntry()
	bad.ID = CapabilityChatterbox
	engineMismatch.Capabilities = []CapabilityEntry{bad}
	if err := ValidateEnrollRequest(&engineMismatch); err == nil {
		t.Fatal("engine/capability mismatch must be rejected")
	}
	badCategory := valid
	badCategory.Capabilities = []CapabilityEntry{aceEntry()}
	badCategory.Capabilities[0].Category = CategoryTTS
	if err := ValidateEnrollRequest(&badCategory); err == nil {
		t.Fatal("wrong category must be rejected")
	}
}

func TestValidatePresenceRequestMatrix(t *testing.T) {
	no := false
	valid := PresenceRequest{
		ProtocolVersion: ProtocolVersion,
		Capabilities: []PresenceEntry{
			{ID: CapabilityAppleSpeech, State: PresenceReady, Accepting: true, ActiveJobs: 0},
			{ID: CapabilityChatterbox, State: PresenceBusy, Accepting: no, ActiveJobs: 1},
			{ID: CapabilityRelay, State: PresencePaused, Accepting: no, ActiveJobs: 0,
				Reason: stringPtr("user_paused")},
			{ID: CapabilityACE, State: PresenceBusy, Accepting: no, ActiveJobs: 0,
				Reason: stringPtr("insufficient_memory")},
		},
	}
	if err := ValidatePresenceRequest(&valid); err != nil {
		t.Fatalf("truthful snapshot rejected: %v", err)
	}
	invalid := []PresenceEntry{
		// Paused must not accept work.
		{ID: CapabilityRelay, State: PresencePaused, Accepting: true, ActiveJobs: 0,
			Reason: stringPtr("user_paused")},
		// Busy waiting needs a resource reason.
		{ID: CapabilityChatterbox, State: PresenceBusy, Accepting: no, ActiveJobs: 0,
			Reason: stringPtr("user_paused")},
		// Busy running must not report a reason.
		{ID: CapabilityChatterbox, State: PresenceBusy, Accepting: no, ActiveJobs: 1,
			Reason: stringPtr("slot_busy")},
		// Ready is idle, accepting, and silent.
		{ID: CapabilityAppleSpeech, State: PresenceReady, Accepting: no, ActiveJobs: 0},
		// Unknown reason.
		{ID: CapabilityAppleSpeech, State: PresenceError, Accepting: no, ActiveJobs: 0,
			Reason: stringPtr("exploded")},
		// active_jobs out of range.
		{ID: CapabilityAppleSpeech, State: PresenceReady, Accepting: true, ActiveJobs: 2},
	}
	for index, entry := range invalid {
		candidate := PresenceRequest{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    []PresenceEntry{entry},
		}
		if err := ValidatePresenceRequest(&candidate); err == nil {
			t.Fatalf("invalid presence entry %d must be rejected", index)
		}
	}
}

func TestValidateLeaseHeartbeatFailAndMetadata(t *testing.T) {
	if err := ValidateLeaseRequest(&LeaseRequest{
		ProtocolVersion: ProtocolVersion, CapabilityID: CapabilityACE, WaitSeconds: 25,
	}); err != nil {
		t.Fatalf("wait=25 rejected: %v", err)
	}
	for _, wait := range []int{-1, 26} {
		if err := ValidateLeaseRequest(&LeaseRequest{
			ProtocolVersion: ProtocolVersion, CapabilityID: CapabilityACE, WaitSeconds: wait,
		}); err == nil {
			t.Fatalf("wait_seconds=%d must be rejected", wait)
		}
	}
	if err := ValidateLeaseRequest(&LeaseRequest{
		ProtocolVersion: ProtocolVersion, CapabilityID: "tts.unknown.v9", WaitSeconds: 0,
	}); err == nil || err.(*Error).Code != CodeUnsupportedCap {
		t.Fatal("unknown capability must be unsupported_capability")
	}
	if err := ValidateHeartbeatRequest(&HeartbeatRequest{
		ProtocolVersion: ProtocolVersion, Attempt: 1, ProgressPercent: 100,
	}); err != nil {
		t.Fatalf("valid heartbeat rejected: %v", err)
	}
	if err := ValidateHeartbeatRequest(&HeartbeatRequest{
		ProtocolVersion: ProtocolVersion, Attempt: 1, ProgressPercent: 101,
	}); err == nil {
		t.Fatal("progress 101 must be rejected")
	}
	for _, code := range []string{
		"canceled", "interrupted", "resource_exhausted", "invalid_payload",
		"setup_required", "execution_failed", "relay_unreachable", "relay_auth",
		"relay_invalid_response", "relay_model_unknown",
	} {
		if err := ValidateFailRequest(&FailRequest{
			ProtocolVersion: ProtocolVersion, Attempt: 1, Code: code, Retryable: true,
		}); err != nil {
			t.Fatalf("failure code %s rejected: %v", code, err)
		}
	}
	if err := ValidateFailRequest(&FailRequest{
		ProtocolVersion: ProtocolVersion, Attempt: 1, Code: "v1.relay_unreachable",
	}); err == nil {
		t.Fatal("protocol-specific failure codes must be rejected")
	}
	metadata := CompleteMetadata{
		ProtocolVersion: ProtocolVersion, Attempt: 1,
		ResultSHA256: strings.Repeat("a", 64),
	}
	if err := ValidateCompleteMetadata(&metadata); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	metadata.ResultSHA256 = strings.ToUpper(metadata.ResultSHA256)
	if err := ValidateCompleteMetadata(&metadata); err == nil {
		t.Fatal("uppercase digest must be rejected")
	}
}

func TestPayloadEncodeDecodeAndHashMismatch(t *testing.T) {
	payload := []byte(`{"render_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","spoken_text":"Demosatz ✓ / ok"}`)
	encoded, digest, err := EncodePayload(payload)
	if err != nil {
		t.Fatalf("EncodePayload failed: %v", err)
	}
	decoded, err := DecodePayload(encoded, digest)
	if err != nil {
		t.Fatalf("DecodePayload failed: %v", err)
	}
	if string(decoded) != string(payload) {
		t.Fatal("payload roundtrip changed bytes")
	}
	if _, err := DecodePayload(base64.StdEncoding.EncodeToString([]byte(`{"x":1}`)), digest); err == nil {
		t.Fatal("hash mismatch must be rejected")
	}
	if _, _, err := EncodePayload([]byte("x")); err != nil {
		t.Fatalf("small payload rejected: %v", err)
	}
	oversize := make([]byte, PayloadMaxBytes+1)
	if _, _, err := EncodePayload(oversize); err == nil {
		t.Fatal("oversize payload must be rejected")
	}
}

func TestCredentialHashingAndTokenForm(t *testing.T) {
	token := strings.Repeat("a", 43)
	if !ValidToken(token) {
		t.Fatal("43-char token must validate")
	}
	if ValidToken(token + "x") {
		t.Fatal("token length must be exact")
	}
	stored := HashToken(token)
	if !ConstantTimeHashEquals(stored, token) {
		t.Fatal("hash comparison must succeed for the same token")
	}
	if ConstantTimeHashEquals(stored, strings.Repeat("b", 43)) {
		t.Fatal("hash comparison must fail for another token")
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	envelope := ErrorEnvelope(CodeLeaseLost, "lease is no longer valid")
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("envelope marshal failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("envelope unmarshal failed: %v", err)
	}
	if decoded["protocol_version"] != ProtocolVersion {
		t.Fatalf("protocol_version = %v", decoded["protocol_version"])
	}
	errObj, ok := decoded["error"].(map[string]any)
	if !ok || errObj["code"] != string(CodeLeaseLost) || errObj["message"] != "lease is no longer valid" {
		t.Fatalf("error object = %v", decoded["error"])
	}
	if len(decoded) != 2 || len(errObj) != 2 {
		t.Fatalf("envelope fields are not exact: %v", decoded)
	}
}

func TestIdentifierAndServiceKindValidation(t *testing.T) {
	if !ValidIdentifier("01ARZ3NDEKTSV4RRFFQ69G5FAV") || !ValidIdentifier("job_1") {
		t.Fatal("opaque identifiers must validate")
	}
	if ValidIdentifier("") || ValidIdentifier(strings.Repeat("a", 129)) {
		t.Fatal("identifier bounds must be enforced")
	}
	if !ValidServiceKind("doublangu") || ValidServiceKind("Doublangu") {
		t.Fatal("service_kind must be lowercase")
	}
}

func stringPtr(value string) *string {
	return &value
}
