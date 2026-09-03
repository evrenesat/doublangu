package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// JobPayload is the canonical immutable analysis job snapshot. It contains
// the full resolved profile (provider types and config fingerprints, canonical
// options) but never an endpoint or secret. Bindings appear in registered
// stage order.
type JobPayload struct {
	ArticleID               string          `json:"article_id"`
	ContentHash             string          `json:"content_hash"`
	AnalysisContractVersion string          `json:"analysis_contract_version"`
	PipelineVersion         string          `json:"pipeline_version"`
	Fresh                   bool            `json:"fresh"`
	Profile                 ProfileSnapshot `json:"profile"`
	ProfileSnapshotHash     string          `json:"profile_snapshot_hash"`
}

// DecodeJobPayload strictly decodes and verifies a job payload: versions,
// registered stage order, canonical snapshot hash, and required fields.
func DecodeJobPayload(data []byte) (JobPayload, error) {
	var payload JobPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return JobPayload{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return JobPayload{}, errors.New("job payload contains trailing JSON")
		}
		return JobPayload{}, fmt.Errorf("job payload contains malformed trailing JSON: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return JobPayload{}, err
	}
	return payload, nil
}

// EncodeJobPayload marshals the canonical payload form.
func EncodeJobPayload(payload JobPayload) ([]byte, error) {
	if err := payload.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// Validate checks every identity and hash before article state may change.
func (p JobPayload) Validate() error {
	for _, field := range []struct {
		name, value string
	}{
		{"article_id", p.ArticleID},
		{"content_hash", p.ContentHash},
		{"analysis_contract_version", p.AnalysisContractVersion},
		{"pipeline_version", p.PipelineVersion},
		{"profile_snapshot_hash", p.ProfileSnapshotHash},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("job payload %s is required", field.name)
		}
	}
	if p.AnalysisContractVersion != AnalysisContractVersion {
		return fmt.Errorf("unsupported analysis contract %q", p.AnalysisContractVersion)
	}
	if p.PipelineVersion != PipelineVersion {
		return fmt.Errorf("unsupported pipeline version %q", p.PipelineVersion)
	}
	if err := p.Profile.Validate(); err != nil {
		return fmt.Errorf("job payload profile: %w", err)
	}
	computed, err := p.Profile.SnapshotHash()
	if err != nil {
		return err
	}
	if computed != p.ProfileSnapshotHash {
		return errors.New("job payload profile snapshot hash does not match the profile")
	}
	return nil
}

// NormalJobIdempotencySuffix is the stable identity suffix shared by queue
// keys: pipeline version, snapshot hash, content hash, and fresh mode.
func (p JobPayload) NormalJobIdempotencySuffix() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	mode := "normal"
	if p.Fresh {
		mode = "fresh"
	}
	return strings.Join([]string{PipelineVersion, p.ProfileSnapshotHash, p.ContentHash, mode}, ":") + ":request:", nil
}
