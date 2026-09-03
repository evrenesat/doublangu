package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RelayMessage is one relay chat message with the same history semantics as
// the direct OpenAI-compatible session.
type RelayMessage struct {
	Role    string
	Content string
}

// RelayChatParams carries one relay turn: model, full message history,
// output schema, and generation bounds.
type RelayChatParams struct {
	Model            string
	Messages         []RelayMessage
	OutputSchema     json.RawMessage
	TemperatureMilli int
	MaxOutputTokens  int
}

// RelayChatOutcome is one executed relay turn plus its durable correlation
// metadata.
type RelayChatOutcome struct {
	Text              string
	ReportedModel     string
	ProviderRequestID string
	FinishReason      string
	UsageJSON         string
	TimingJSON        string
	RelayJobID        string
	RelayRequestID    string
	Model             string
}

// RelayExecutor is the narrow boundary the relay provider needs. It is
// implemented by an adapter around the relay service, so this package never
// imports the jobs, store, workers, or media packages.
type RelayExecutor interface {
	ChatCompletion(context.Context, RelayChatParams) (RelayChatOutcome, error)
	ListRelayModels(context.Context) ([]Model, error)
}

// macRelayProvider is the configured, enabled relay transport. Timeouts,
// catalogs, and completions all flow through the shared relay executor;
// this instance holds no endpoint or secret.
type macRelayProvider struct {
	descriptor ProviderDescriptor
	timeout    time.Duration
	relay      RelayExecutor
}

func (p *macRelayProvider) Descriptor() ProviderDescriptor { return p.descriptor }

// ListModels delegates through the relay executor. An empty relay model
// list maps to CodeUnavailable, matching the direct provider's catalog
// behavior.
func (p *macRelayProvider) ListModels(ctx context.Context) ([]Model, error) {
	if p.relay == nil {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("mac_relay executor is unavailable")}
	}
	models, err := p.relay.ListRelayModels(ctx)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("model catalog returned no models")}
	}
	return models, nil
}

func (p *macRelayProvider) OpenSession(ctx context.Context, binding ResolvedBinding) (Session, error) {
	if binding.ProviderType != ProviderTypeMacRelay {
		return nil, fmt.Errorf("provider %q is not mac_relay", binding.ProviderID)
	}
	var options configOpenAIOptions
	if err := decodeOpenAIOptions(binding.Options, &options); err != nil {
		return nil, err
	}
	if p.relay == nil {
		return nil, &Error{Code: CodeUnavailable, Err: errors.New("mac_relay executor is unavailable")}
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	return &macRelaySession{provider: p, binding: binding, options: options, timeout: timeout}, nil
}

// macRelaySession preserves the message history across turns exactly like
// the direct session, so corrective turns see their own rejected output.
// The provider timeout covers the entire enqueue, worker execution, and
// result wait.
type macRelaySession struct {
	provider *macRelayProvider
	binding  ResolvedBinding
	options  configOpenAIOptions
	timeout  time.Duration
	messages []RelayMessage
}

func (s *macRelaySession) Turn(ctx context.Context, request TurnRequest) (Completion, error) {
	messages := append(append([]RelayMessage(nil), s.messages...), RelayMessage{Role: "user", Content: request.Prompt})
	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	outcome, err := s.provider.relay.ChatCompletion(runCtx, RelayChatParams{
		Model:            s.binding.ModelID,
		Messages:         messages,
		OutputSchema:     request.OutputSchema,
		TemperatureMilli: s.options.TemperatureMilli,
		MaxOutputTokens:  s.options.MaxOutputTokens,
	})
	if err != nil {
		// The provider timeout covers the whole relay round trip. A
		// deadline always reports timeout unless the caller itself
		// canceled, which stays a bare cancellation so the pipeline's
		// lease-loss path wins.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != context.Canceled {
			return Completion{}, &Error{Code: CodeTimeout, Err: err}
		}
		return Completion{}, err
	}
	s.messages = append(s.messages,
		RelayMessage{Role: "user", Content: request.Prompt},
		RelayMessage{Role: "assistant", Content: outcome.Text},
	)
	metadata, err := relayMetadataJSON(outcome)
	if err != nil {
		return Completion{}, &Error{Code: CodeProtocol, Err: err}
	}
	// RequestID keeps the existing provider-request-id semantics.
	return Completion{
		Text: outcome.Text, ReportedModel: outcome.ReportedModel,
		RequestID: outcome.ProviderRequestID, FinishReason: outcome.FinishReason,
		UsageJSON: outcome.UsageJSON, TimingJSON: outcome.TimingJSON,
		ProviderMetadataJSON: metadata,
	}, nil
}

func (s *macRelaySession) Close() error {
	s.messages = nil
	return nil
}

// relayMetadataJSON encodes the bounded stage provenance correlation: relay
// job/request ids plus the reported model facts.
func relayMetadataJSON(outcome RelayChatOutcome) (string, error) {
	encoded, err := json.Marshal(map[string]any{
		"relay_job_id":        outcome.RelayJobID,
		"relay_request_id":    outcome.RelayRequestID,
		"provider_request_id": outcome.ProviderRequestID,
		"model":               outcome.Model,
		"finish_reason":       outcome.FinishReason,
	})
	if err != nil {
		return "", err
	}
	if len(encoded) > maxMetadataBytes {
		return "", errors.New("relay metadata exceeds the accepted limit")
	}
	return string(encoded), nil
}
