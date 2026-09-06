package localworker

import (
	"encoding/json"
	"fmt"
	"strings"

	"doublangu/internal/speech"
)

// Capability to legacy job-type mapping. Each common capability dispatches to
// its exact existing job type; broad categories are never authorization or
// concurrency keys.
const (
	JobTypeAVSpeech   = "tts.avspeech.v1"
	JobTypeChatterbox = "tts.chatterbox.v3"
	JobTypeRelay      = "llm.relay.v1"
)

// JobTypeForCapability maps a common capability to its exact job type.
func JobTypeForCapability(capabilityID string) (string, error) {
	switch capabilityID {
	case CapabilityAppleSpeech:
		return JobTypeAVSpeech, nil
	case CapabilityChatterbox:
		return JobTypeChatterbox, nil
	case CapabilityRelay:
		return JobTypeRelay, nil
	}
	return "", NewError(CodeUnsupportedCap, "capability is not supported")
}

// Product failure codes produced by mapping common failure codes. Every
// mapped value passes the existing speech/relay v1.* validators unchanged.
const (
	ProductCodeCanceled          = "v1.canceled"
	ProductCodeInterrupted       = "v1.interrupted"
	ProductCodeResourceExhausted = "v1.resource_exhausted"
	ProductCodeInvalidPayload    = "v1.invalid_payload"
	ProductCodeSetupRequired     = "v1.setup_required"
	ProductCodeExecutionFailed   = "v1.execution_failed"
	ProductCodeRelayUnreachable  = "v1.relay_unreachable"
	ProductCodeRelayAuth         = "v1.relay_auth"
	ProductCodeRelayInvalidResp  = "v1.relay_invalid_response"
	ProductCodeRelayModelUnknown = "v1.relay_model_unknown"
	ProductCodeRelayCanceled     = "v1.relay_canceled"
)

// MapFailureToProductCode maps a common failure code to the existing product
// code for the workload. Relay jobs map cancellation to the relay-specific
// code. Unknown codes fail validation.
func MapFailureToProductCode(failureCode string, isRelay bool) (string, error) {
	if !ValidFailureCode(failureCode) {
		return "", NewError(CodeInvalidRequest, "failure code is not allowlisted")
	}
	if isRelay {
		switch FailureCode(failureCode) {
		case FailureCanceled:
			return ProductCodeRelayCanceled, nil
		case FailureRelayUnreachable:
			return ProductCodeRelayUnreachable, nil
		case FailureRelayAuth:
			return ProductCodeRelayAuth, nil
		case FailureRelayInvalidResp:
			return ProductCodeRelayInvalidResp, nil
		case FailureRelayModelUnknown:
			return ProductCodeRelayModelUnknown, nil
		case FailureInterrupted:
			return ProductCodeInterrupted, nil
		case FailureResourceExhausted:
			return ProductCodeResourceExhausted, nil
		case FailureInvalidPayload:
			return ProductCodeInvalidPayload, nil
		case FailureExecutionFailed:
			return ProductCodeExecutionFailed, nil
		}
		return "", NewError(CodeInvalidRequest, "failure code is not allowlisted")
	}
	switch FailureCode(failureCode) {
	case FailureCanceled:
		return ProductCodeCanceled, nil
	case FailureRelayUnreachable, FailureRelayAuth, FailureRelayInvalidResp,
		FailureRelayModelUnknown:
		return "", NewError(CodeInvalidRequest, "relay failure code is not valid for this workload")
	case FailureInterrupted:
		return ProductCodeInterrupted, nil
	case FailureResourceExhausted:
		return ProductCodeResourceExhausted, nil
	case FailureInvalidPayload:
		return ProductCodeInvalidPayload, nil
	case FailureSetupRequired:
		return ProductCodeSetupRequired, nil
	case FailureExecutionFailed:
		return ProductCodeExecutionFailed, nil
	}
	return "", NewError(CodeInvalidRequest, "failure code is not allowlisted")
}

// TtsLimits mirrors the leased audio limits.
type TtsLimits struct {
	MaxBytes      int64 `json:"max_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

// TtsProfile mirrors the existing immutable speech profile. ReferenceAudioHash
// is null for engines without one.
type TtsProfile struct {
	ID                 string  `json:"id,omitempty"`
	Engine             string  `json:"engine"`
	ModelRevision      string  `json:"model_revision"`
	Language           string  `json:"language"`
	VoiceIdentifier    string  `json:"voice_identifier"`
	ReferenceAudioHash *string `json:"reference_audio_hash"`
	SpeedMilli         int     `json:"speed_milli"`
	PitchCents         int     `json:"pitch_cents"`
	MappingVersion     string  `json:"mapping_version"`
	MIMEType           string  `json:"mime_type"`
	Codec              string  `json:"codec"`
	SampleRateHz       int     `json:"sample_rate_hz"`
	Channels           int     `json:"channels"`
	Active             bool    `json:"active"`
	CreatedAt          string  `json:"created_at,omitempty"`
	UpdatedAt          string  `json:"updated_at,omitempty"`
}

// TtsPayload is the common TTS lease payload: the existing speech lease's
// domain fields without transport fields.
type TtsPayload struct {
	RenderID                string     `json:"render_id"`
	RequestHash             string     `json:"request_hash"`
	SpeechUnitID            string     `json:"speech_unit_id"`
	Language                string     `json:"language"`
	UnitKind                string     `json:"unit_kind"`
	SpokenText              string     `json:"spoken_text"`
	ContextPronunciationKey *string    `json:"context_pronunciation_key"`
	Profile                 TtsProfile `json:"profile"`
	Limits                  TtsLimits  `json:"limits"`
}

// TtsPayloadFromLeaseDomain builds exact payload bytes from the existing
// speech lease domain fields. Domain hashes and profile identity are
// preserved byte-for-byte; the caller's existing validators stay
// authoritative.
func TtsPayloadFromLeaseDomain(
	renderID, requestHash, speechUnitID, language, unitKind, spokenText, contextKey string,
	profile speech.Profile,
	maxBytes, maxDurationMS int64,
) ([]byte, error) {
	var reference *string
	if profile.ReferenceAudioHash != "" {
		reference = &profile.ReferenceAudioHash
	}
	var pronunciation *string
	if contextKey != "" {
		pronunciation = &contextKey
	}
	payload := TtsPayload{
		RenderID:                renderID,
		RequestHash:             requestHash,
		SpeechUnitID:            speechUnitID,
		Language:                language,
		UnitKind:                unitKind,
		SpokenText:              spokenText,
		ContextPronunciationKey: pronunciation,
		Profile: TtsProfile{
			ID:                 profile.ID.String(),
			Engine:             profile.Engine,
			ModelRevision:      profile.ModelRevision,
			Language:           profile.Language,
			VoiceIdentifier:    profile.VoiceIdentifier,
			ReferenceAudioHash: reference,
			SpeedMilli:         profile.SpeedMilli,
			PitchCents:         profile.PitchCents,
			MappingVersion:     profile.MappingVersion,
			MIMEType:           profile.MIMEType,
			Codec:              profile.Codec,
			SampleRateHz:       profile.SampleRateHz,
			Channels:           profile.Channels,
			Active:             profile.Active,
			CreatedAt:          profile.CreatedAt,
			UpdatedAt:          profile.UpdatedAt,
		},
		Limits: TtsLimits{MaxBytes: maxBytes, MaxDurationMS: maxDurationMS},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("localworker: tts payload marshal: %w", err)
	}
	if len(encoded) > PayloadMaxBytes {
		return nil, NewError(CodePayloadTooLarge, "payload exceeds the byte bound")
	}
	return encoded, nil
}

// RelayEnvelope is the discriminating head of the exact stored llm.relay.v1
// request bytes. The nested protocol_version is a legacy domain payload
// version, not the outer transport version.
type RelayEnvelope struct {
	ProtocolVersion string `json:"protocol_version"`
	Operation       string `json:"operation"`
	RequestID       string `json:"request_id"`
}

// RelayOperations is the supported legacy relay operation set.
var RelayOperations = map[string]bool{
	"chat_completion": true,
	"list_models":     true,
}

// DecodeRelayPayload validates the relay envelope head and returns the exact
// stored payload bytes for authenticated forwarding. The bytes are never
// reserialized: the domain request hash is computed over them.
func DecodeRelayPayload(payload []byte) (*RelayEnvelope, error) {
	var envelope RelayEnvelope
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	if err := decoder.Decode(&envelope); err != nil {
		return nil, NewError(CodeInvalidRequest, "relay payload is not JSON")
	}
	if !RelayOperations[envelope.Operation] {
		return nil, NewError(CodeInvalidRequest, "relay operation is not supported")
	}
	if envelope.ProtocolVersion != speech.ProtocolVersion {
		return nil, NewError(CodeInvalidRequest, "relay payload version is unsupported")
	}
	if envelope.RequestID == "" || len(envelope.RequestID) > 256 {
		return nil, NewError(CodeInvalidRequest, "relay request_id is invalid")
	}
	return &envelope, nil
}

// ArtifactPartName and ResultPartName are the common completion part names.
const (
	MetadataPartName = "metadata"
	ResultPartName   = "result"
	ArtifactPartName = "artifact"
	ArtifactFileName = "audio.m4a"
	ArtifactMIMEType = speech.AudioMIME
)
