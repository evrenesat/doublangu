// Package speech owns reusable speech units, immutable audio renders, and
// article narration bindings. It has no browser or provider dependencies.
package speech

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"doublangu/internal/library"
	"golang.org/x/text/unicode/norm"
)

const (
	ProtocolVersion  = "speech-worker.v1"
	AVSpeechEngine   = "avspeech"
	ChatterboxEngine = "chatterbox"

	UnitWord     = "word"
	UnitPhrase   = "phrase"
	UnitSentence = "sentence"

	RetentionLexical   = "lexical_permanent"
	RetentionNarration = "article_narration"

	RenderQueued     = "queued"
	RenderGenerating = "generating"
	RenderReady      = "ready"
	RenderFailed     = "failed"
	RenderPurged     = "purged"

	NarrationNotRequested = "not_requested"
	NarrationQueued       = "queued"
	NarrationGenerating   = "generating"
	NarrationPartial      = "partial"
	NarrationReady        = "ready"
	NarrationFailed       = "failed"
	NarrationPurged       = "purged"

	AudioMIME                 = "audio/mp4"
	AudioCodec                = "aac-lc"
	AudioSampleRate           = 24000
	AudioChannels             = 1
	AudioNormalizationVersion = "audio-normalization.v1"
)

type Unit struct {
	ID                      library.ULID  `json:"id"`
	Language                string        `json:"language"`
	UnitKind                string        `json:"unit_kind"`
	SpokenText              string        `json:"spoken_text"`
	NormalizedTextHash      string        `json:"normalized_text_hash"`
	ContextPronunciationKey string        `json:"context_pronunciation_key"`
	SemanticSenseID         *library.ULID `json:"semantic_sense_id"`
	CreatedAt               string        `json:"created_at"`
}

type Profile struct {
	ID                 library.ULID `json:"id"`
	Engine             string       `json:"engine"`
	ModelRevision      string       `json:"model_revision"`
	Language           string       `json:"language"`
	VoiceIdentifier    string       `json:"voice_identifier"`
	ReferenceAudioHash string       `json:"reference_audio_hash"`
	SpeedMilli         int          `json:"speed_milli"`
	PitchCents         int          `json:"pitch_cents"`
	MappingVersion     string       `json:"mapping_version"`
	MIMEType           string       `json:"mime_type"`
	Codec              string       `json:"codec"`
	SampleRateHz       int          `json:"sample_rate_hz"`
	Channels           int          `json:"channels"`
	Active             bool         `json:"active"`
	CreatedAt          string       `json:"created_at"`
	UpdatedAt          string       `json:"updated_at"`
}

type Render struct {
	ID              library.ULID `json:"id"`
	SpeechUnitID    library.ULID `json:"speech_unit_id"`
	SpeechProfileID library.ULID `json:"speech_profile_id"`
	RequestHash     string       `json:"request_hash"`
	RetentionClass  string       `json:"retention_class"`
	State           string       `json:"state"`
	ErrorCode       string       `json:"error_code"`
	DurationMS      int64        `json:"duration_ms"`
	SizeBytes       int64        `json:"size_bytes"`
	CreatedAt       string       `json:"created_at"`
	UpdatedAt       string       `json:"updated_at"`
	ReadyAt         string       `json:"ready_at"`
	BlobDigest      string       `json:"-"`
}

type ArtifactMetadata struct {
	RequestHash  string `json:"request_hash"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	MIMEType     string `json:"mime_type"`
	Codec        string `json:"codec"`
	SampleRateHz int    `json:"sample_rate_hz"`
	Channels     int    `json:"channels"`
	DurationMS   int64  `json:"duration_ms"`
}

type AudioLimits struct {
	MaxBytes      int64 `json:"max_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

func Limits(unitKind string) AudioLimits {
	if unitKind == UnitSentence {
		return AudioLimits{MaxBytes: 64 << 20, MaxDurationMS: 180000}
	}
	if unitKind == UnitPhrase {
		return AudioLimits{MaxBytes: 2 << 20, MaxDurationMS: 30000}
	}
	return AudioLimits{MaxBytes: 2 << 20, MaxDurationMS: 15000}
}

func ValidateProfile(profile Profile) error {
	if profile.Engine != AVSpeechEngine && profile.Engine != ChatterboxEngine {
		return fmt.Errorf("unsupported speech engine %q", profile.Engine)
	}
	if profile.Language == "" || profile.ModelRevision == "" || profile.VoiceIdentifier == "" || profile.MappingVersion == "" {
		return errors.New("speech profile identity is incomplete")
	}
	if profile.SpeedMilli <= 0 || profile.SampleRateHz <= 0 || profile.Channels <= 0 || profile.MIMEType == "" || profile.Codec == "" {
		return errors.New("speech profile has invalid media settings")
	}
	if profile.MIMEType != AudioMIME || profile.Codec != AudioCodec || profile.SampleRateHz != AudioSampleRate || profile.Channels != AudioChannels {
		return errors.New("speech profile must be mono 24kHz AAC-LC M4A")
	}
	return nil
}

func NormalizeTextHash(value string) (string, error) {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return "", errors.New("spoken text must be non-empty valid UTF-8")
	}
	// Speech identity intentionally preserves punctuation and accents while
	// collapsing only surrounding/duplicate Unicode whitespace.
	value = norm.NFC.String(strings.Join(strings.Fields(value), " "))
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]), nil
}

// RequestHash is a versioned length-delimited identity over every byte-affecting
// speech field. It must not depend on JSON map ordering.
func RequestHash(unit Unit, profile Profile) string {
	var b bytes.Buffer
	part := func(value string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(value)))
		b.Write(n[:])
		b.WriteString(value)
	}
	integer := func(value int64) { var n [8]byte; binary.BigEndian.PutUint64(n[:], uint64(value)); b.Write(n[:]) }
	part("doublangu.audio-request.v1")
	part(unit.SpokenText)
	part(unit.Language)
	part(unit.UnitKind)
	part(unit.ContextPronunciationKey)
	part(profile.Engine)
	part(profile.ModelRevision)
	part(profile.Language)
	part(profile.VoiceIdentifier)
	part(profile.ReferenceAudioHash)
	integer(int64(profile.SpeedMilli))
	integer(int64(profile.PitchCents))
	part(profile.MappingVersion)
	part(profile.Codec)
	part(profile.MIMEType)
	integer(int64(profile.SampleRateHz))
	integer(int64(profile.Channels))
	part(AudioNormalizationVersion)
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

func ValidateArtifact(metadata ArtifactMetadata, dataSize int64, signature []byte, expectedRequestHash string, unitKind string) error {
	if metadata.RequestHash != expectedRequestHash {
		return errors.New("audio request hash does not match job")
	}
	if len(metadata.SHA256) != 64 {
		return errors.New("audio sha256 must be a lowercase SHA-256 digest")
	}
	if strings.ToLower(metadata.SHA256) != metadata.SHA256 {
		return errors.New("audio sha256 must be lowercase")
	}
	if metadata.SizeBytes <= 0 || dataSize != metadata.SizeBytes {
		return errors.New("audio declared size does not match upload")
	}
	if metadata.MIMEType != AudioMIME || metadata.Codec != AudioCodec || metadata.SampleRateHz != AudioSampleRate || metadata.Channels != AudioChannels {
		return errors.New("audio metadata is not the required mono 24kHz AAC-LC M4A")
	}
	if len(signature) < 8 || string(signature[4:8]) != "ftyp" {
		return errors.New("audio is not an ISO-BMFF file")
	}
	limits := Limits(unitKind)
	if dataSize > limits.MaxBytes || metadata.DurationMS <= 0 || metadata.DurationMS > limits.MaxDurationMS {
		return errors.New("audio exceeds unit limits")
	}
	return nil
}

type Narration struct {
	ArticleID        library.ULID    `json:"article_id"`
	Status           string          `json:"status"`
	ErrorCode        string          `json:"error_code"`
	SentenceCount    int             `json:"sentence_count"`
	ReadyCount       int             `json:"ready_count"`
	DurationMS       int64           `json:"duration_ms"`
	SizeBytes        int64           `json:"size_bytes"`
	ReclaimableBytes int64           `json:"reclaimable_bytes"`
	Clips            []NarrationClip `json:"clips"`
}

type NarrationClip struct {
	SentenceID    library.ULID `json:"sentence_id"`
	SequenceIndex int          `json:"sequence_index"`
	Audio         *AudioRef    `json:"audio"`
}

type AudioRef struct {
	RenderID   library.ULID `json:"render_id"`
	URL        string       `json:"url"`
	Ready      bool         `json:"ready"`
	DurationMS int64        `json:"duration_ms"`
	SizeBytes  int64        `json:"size_bytes"`
	ErrorCode  string       `json:"error_code"`
}
