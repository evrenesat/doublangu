// Package pipeline owns the fixed two-stage analysis pipeline identities,
// provider-neutral binding/profile snapshots, and canonical domain-separated
// hashes. It deliberately imports no annotator, analysis, reader, or
// semantics package: every dependent layer can rely on these identities
// without creating an import cycle.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Independent pipeline identities. The merged persisted contract
// (reader.analysis.v3) is unchanged by splitting its provider production
// path; these identities version the stages and their prompts instead.
const (
	AnalysisContractVersion     = "reader.analysis.v3"
	PipelineVersion             = "reader-analysis-pipeline.v1"
	LinguisticContractVersion   = "reader.linguistic.v1"
	LinguisticPromptVersion     = "reader-linguistic-prompt.v1"
	TranslationContractVersion  = "reader.translation.v1"
	TranslationPromptVersion    = "reader-translation-prompt.v1"
	ProfileSnapshotHashDomain   = "doublangu.profile-snapshot.v1"
	OptionsHashDomain           = "doublangu.binding-options.v1"
	AnalysisContractPlaceholder = "legacy.analysis"
)

// StageID names one code-defined pipeline stage. The database stores stage
// ids as plain text; only stages registered by this running binary are valid.
type StageID string

const (
	StageLinguisticAnalysis StageID = "linguistic_analysis"
	StageTranslation        StageID = "translation"
)

// Valid reports whether id is a registered stage.
func (id StageID) Valid() bool {
	return id == StageLinguisticAnalysis || id == StageTranslation
}

// StageContracts returns the exact contract and prompt versions for a stage.
func StageContracts(id StageID) (contract, prompt string, ok bool) {
	switch id {
	case StageLinguisticAnalysis:
		return LinguisticContractVersion, LinguisticPromptVersion, true
	case StageTranslation:
		return TranslationContractVersion, TranslationPromptVersion, true
	default:
		return "", "", false
	}
}

// RegisteredStages returns the fixed pipeline stage order. Bindings and
// snapshots must follow this order; the registry fixes order, dependency,
// and contract identity in Go.
func RegisteredStages() []StageID {
	return []StageID{StageLinguisticAnalysis, StageTranslation}
}

// BindingSnapshot is the provider-neutral immutable binding of one stage to a
// provider/model/options tuple. It contains no endpoint and no secret; the
// provider config fingerprint names the trusted connection identity instead.
type BindingSnapshot struct {
	StageID                   StageID         `json:"stage_id"`
	ProviderID                string          `json:"provider_id"`
	ProviderType              string          `json:"provider_type"`
	ProviderConfigFingerprint string          `json:"provider_config_fingerprint"`
	ModelID                   string          `json:"model_id"`
	Options                   json.RawMessage `json:"options"`
	OptionsHash               string          `json:"options_hash"`
	ContractVersion           string          `json:"contract_version"`
	PromptVersion             string          `json:"prompt_version"`
}

// ProfileSnapshot is the immutable profile value resolved before any queue or
// article state changes. Bindings appear in registered stage order.
type ProfileSnapshot struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Bindings []BindingSnapshot `json:"bindings"`
}

// StageOrder compares bindings by the registered stage order.
func StageOrder(left, right StageID) int {
	for _, id := range RegisteredStages() {
		switch {
		case id == left && id == right:
			return 0
		case id == left:
			return -1
		case id == right:
			return 1
		}
	}
	if left == right {
		return 0
	}
	return strings.Compare(string(left), string(right))
}

// ValidateProfileName checks the owner-facing profile name rule: trimmed,
// 1-80 Unicode scalar values, no control characters.
func ValidateProfileName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("profile name is required")
	}
	if !utf8.ValidString(trimmed) {
		return errors.New("profile name must be valid UTF-8")
	}
	if utf8.RuneCountInString(trimmed) > 80 {
		return errors.New("profile name must be at most 80 characters")
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return errors.New("profile name must not contain control characters")
		}
	}
	return nil
}

// Validate checks stage identity, versions, required fields, and that exactly
// one binding exists per registered MVP stage. Bindings must already be
// ordered by the caller (see SortBindings).
func (p ProfileSnapshot) Validate() error {
	if err := ValidateProfileName(p.Name); err != nil {
		return err
	}
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("profile id is required")
	}
	if len(p.Bindings) != len(RegisteredStages()) {
		return fmt.Errorf("profile must bind exactly %d stages, got %d", len(RegisteredStages()), len(p.Bindings))
	}
	seen := make(map[StageID]bool, len(p.Bindings))
	previous := StageID("")
	for index, binding := range p.Bindings {
		if !binding.StageID.Valid() {
			return fmt.Errorf("bindings[%d] has unregistered stage %q", index, binding.StageID)
		}
		if seen[binding.StageID] {
			return fmt.Errorf("bindings[%d] duplicates stage %q", index, binding.StageID)
		}
		seen[binding.StageID] = true
		if index > 0 && StageOrder(previous, binding.StageID) >= 0 {
			return fmt.Errorf("bindings are not in registered stage order at index %d", index)
		}
		previous = binding.StageID
		if err := binding.Validate(); err != nil {
			return fmt.Errorf("bindings[%d]: %w", index, err)
		}
	}
	for _, stage := range RegisteredStages() {
		if !seen[stage] {
			return fmt.Errorf("profile is missing stage %q", stage)
		}
	}
	return nil
}

// Validate checks one binding's identity and version consistency. Options
// content and the options hash are validated by the provider option codecs;
// this layer only requires the canonical hash to be present and well-formed.
func (b BindingSnapshot) Validate() error {
	if !b.StageID.Valid() {
		return fmt.Errorf("unregistered stage %q", b.StageID)
	}
	for _, field := range []struct {
		name, value string
	}{
		{"provider_id", b.ProviderID},
		{"provider_type", b.ProviderType},
		{"provider_config_fingerprint", b.ProviderConfigFingerprint},
		{"model_id", b.ModelID},
		{"options_hash", b.OptionsHash},
		{"contract_version", b.ContractVersion},
		{"prompt_version", b.PromptVersion},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	contract, prompt, ok := StageContracts(b.StageID)
	if !ok {
		return fmt.Errorf("unregistered stage %q", b.StageID)
	}
	if b.ContractVersion != contract || b.PromptVersion != prompt {
		return fmt.Errorf("stage %q versions do not match the registered identity", b.StageID)
	}
	if len(bytes.TrimSpace(b.Options)) == 0 || !json.Valid(b.Options) {
		return errors.New("options must be valid canonical JSON")
	}
	return nil
}

// SortBindings returns a copy of bindings ordered by the registered stage
// order. Duplicates are an error because a profile must bind each stage once.
func SortBindings(bindings []BindingSnapshot) ([]BindingSnapshot, error) {
	sorted := append([]BindingSnapshot(nil), bindings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return StageOrder(sorted[i].StageID, sorted[j].StageID) < 0
	})
	for index := 1; index < len(sorted); index++ {
		if sorted[index-1].StageID == sorted[index].StageID {
			return nil, fmt.Errorf("duplicate binding for stage %q", sorted[index].StageID)
		}
	}
	return sorted, nil
}

// OptionsHashOf returns the domain-separated SHA-256 over canonical options
// JSON. Producers must supply already-canonical JSON (encoding/json map key
// ordering); a compacted copy is hashed so whitespace differences never
// change an identity.
func OptionsHashOf(options json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(options)) == 0 || !json.Valid(options) {
		return "", errors.New("options must be valid JSON")
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, options); err != nil {
		return "", err
	}
	return hashParts(OptionsHashDomain, compacted.Bytes()), nil
}

// SnapshotHash returns the domain-separated SHA-256 over the canonical JSON
// of the complete normalized profile (bindings sorted in registered order).
func (p ProfileSnapshot) SnapshotHash() (string, error) {
	sorted, err := SortBindings(p.Bindings)
	if err != nil {
		return "", err
	}
	normalized := p
	normalized.Bindings = append([]BindingSnapshot(nil), sorted...)
	// Options JSON must contribute canonically: compact each binding's options
	// so whitespace-only differences never change the snapshot identity.
	for index := range normalized.Bindings {
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, normalized.Bindings[index].Options); err != nil {
			return "", err
		}
		normalized.Bindings[index].Options = compacted.Bytes()
	}
	if err := normalized.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return hashParts(ProfileSnapshotHashDomain, encoded), nil
}

func hashParts(domain string, payloads ...[]byte) string {
	hash := sha256.New()
	hash.Write([]byte(domain))
	hash.Write([]byte{0})
	for _, payload := range payloads {
		var length [8]byte
		for index := range length {
			length[7-index] = byte(len(payload) >> (8 * index))
		}
		hash.Write(length[:])
		hash.Write(payload)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
