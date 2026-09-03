// Package-level provider transport boundary for the configurable analysis
// pipeline: provider descriptors, model catalogs, resolved bindings, and the
// per-stage session contract. Transport implementations (Codex app-server and
// OpenAI-compatible HTTP) live in codex_session.go and openai_compatible.go.
package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"doublangu/internal/config"
	"doublangu/internal/pipeline"
)

// ProviderType constants mirror the configuration vocabulary.
const (
	ProviderTypeCodexAppServer   = config.ProviderTypeCodexAppServer
	ProviderTypeOpenAICompatible = config.ProviderTypeOpenAICompatible
)

// ProviderDescriptor is the sanitized owner-visible provider card. It never
// contains an endpoint, environment variable name, or secret.
type ProviderDescriptor struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	EndpointLabel     string `json:"endpoint_label"`
	Type              string `json:"type"`
	Enabled           bool   `json:"enabled"`
	ConfigFingerprint string `json:"config_fingerprint"`
	RequestTimeoutMS  int64  `json:"request_timeout_ms"`
}

// ResolvedBinding is the transport value built from a validated
// pipeline.BindingSnapshot. It contains canonical options but no secret.
type ResolvedBinding struct {
	StageID           pipeline.StageID
	ProviderID        string
	ProviderType      string
	ConfigFingerprint string
	ModelID           string
	Options           json.RawMessage
	OptionsHash       string
	ContractVersion   string
	PromptVersion     string
}

// ResolveBinding validates and canonicalizes one snapshot binding into the
// transport value. Options are decoded through the provider-specific codec so
// unknown fields, missing fields, and out-of-range values fail here.
func ResolveBinding(snapshot pipeline.BindingSnapshot) (ResolvedBinding, error) {
	if err := snapshot.Validate(); err != nil {
		return ResolvedBinding{}, err
	}
	canonical, err := config.CanonicalizeProviderOptions(snapshot.ProviderType, snapshot.Options)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("binding %s options: %w", snapshot.StageID, err)
	}
	optionsHash, err := pipeline.OptionsHashOf(canonical)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if snapshot.OptionsHash != "" && snapshot.OptionsHash != optionsHash {
		return ResolvedBinding{}, errors.New("binding options hash does not match canonical options")
	}
	return ResolvedBinding{
		StageID: snapshot.StageID, ProviderID: snapshot.ProviderID,
		ProviderType: snapshot.ProviderType, ConfigFingerprint: snapshot.ProviderConfigFingerprint,
		ModelID: snapshot.ModelID, Options: canonical, OptionsHash: optionsHash,
		ContractVersion: snapshot.ContractVersion, PromptVersion: snapshot.PromptVersion,
	}, nil
}

// TurnRequest carries only the stage id, prompt, and exact output schema.
type TurnRequest struct {
	StageID      pipeline.StageID
	Prompt       string
	OutputSchema json.RawMessage
}

// Completion is one completed assistant turn result. It never includes
// secrets or hidden reasoning; usage/timing and provider metadata are
// canonical bounded JSON.
type Completion struct {
	Text                 string
	ReportedModel        string
	RequestID            string
	FinishReason         string
	UsageJSON            string
	TimingJSON           string
	ProviderMetadataJSON string
	ProviderErrorJSON    string
	StderrExcerpt        string
}

// Session is one logical stage session bound to a paragraph. Corrections run
// in the same logical session (the same Codex ephemeral thread or the same
// OpenAI-compatible message history).
type Session interface {
	Turn(context.Context, TurnRequest) (Completion, error)
	Close() error
}

// Provider is a configured, enabled transport.
type Provider interface {
	Descriptor() ProviderDescriptor
	ListModels(context.Context) ([]Model, error)
	OpenSession(context.Context, ResolvedBinding) (Session, error)
}

// Registry holds every configured provider by id. Disabled providers appear
// in Descriptors but have no instance.
type Registry struct {
	descriptors []ProviderDescriptor
	instances   map[string]Provider
	byID        map[string]config.ProviderEntry
}

// NewRegistry builds the provider registry from the strict configuration.
// secrets resolves the referenced environment values for enabled
// openai-compatible providers; the resolved secret lives only inside the
// provider instance and is never serialized.
func NewRegistry(file *config.ProviderConfigFile, codexCodexBinary string, codexTimeout time.Duration, secrets func(string) (string, error)) (*Registry, error) {
	if file == nil {
		return nil, errors.New("provider config is required")
	}
	registry := &Registry{
		instances: make(map[string]Provider, len(file.Providers)),
		byID:      make(map[string]config.ProviderEntry, len(file.Providers)),
	}
	for _, entry := range file.Providers {
		descriptor := ProviderDescriptor{
			ID: entry.ID, Label: entry.Label, EndpointLabel: entry.EndpointLabel,
			Type: entry.Type, Enabled: entry.Enabled,
			ConfigFingerprint: config.ProviderConfigFingerprint(entry),
			RequestTimeoutMS:  int64(config.ResolveTimeoutSeconds(entry)) * 1000,
		}
		registry.descriptors = append(registry.descriptors, descriptor)
		registry.byID[entry.ID] = entry
		if !entry.Enabled {
			continue
		}
		switch entry.Type {
		case ProviderTypeCodexAppServer:
			registry.instances[entry.ID] = &codexStageProvider{
				descriptor: descriptor, binary: codexCodexBinary, timeout: codexTimeout,
			}
		case ProviderTypeOpenAICompatible:
			if secrets == nil {
				return nil, fmt.Errorf("provider %q requires a secret resolver", entry.ID)
			}
			secretValue, err := secrets(entry.APIKeyEnv)
			if err != nil || secretValue == "" {
				return nil, fmt.Errorf("provider %q secret resolution failed", entry.ID)
			}
			timeout := time.Duration(config.ResolveTimeoutSeconds(entry)) * time.Second
			registry.instances[entry.ID] = &openAICompatibleProvider{
				descriptor: descriptor, baseURL: entry.BaseURL,
				apiKey: secretValue, timeout: timeout, client: newOpenAIHTTPClient(timeout),
			}
		default:
			return nil, fmt.Errorf("provider %q has unknown type %q", entry.ID, entry.Type)
		}
	}
	return registry, nil
}

// Descriptors returns the sanitized provider cards ordered by id.
func (r *Registry) Descriptors() []ProviderDescriptor {
	if r == nil {
		return nil
	}
	descriptors := append([]ProviderDescriptor(nil), r.descriptors...)
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return descriptors
}

// Provider returns the enabled instance for an id.
func (r *Registry) Provider(id string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.instances[id]
	return provider, ok
}

// ListModels fetches the catalog for one enabled provider.
func (r *Registry) ListModels(ctx context.Context, providerID string) ([]Model, error) {
	provider, ok := r.Provider(providerID)
	if !ok {
		return nil, &Error{Code: CodeUnavailable, Err: fmt.Errorf("provider %q is not available", providerID)}
	}
	return provider.ListModels(ctx)
}
