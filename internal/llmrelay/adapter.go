package llmrelay

import (
	"context"
	"errors"
	"fmt"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/library"
)

// Service implements annotator.RelayExecutor without importing the jobs,
// store, workers, or media packages from the annotator side.
var _ annotator.RelayExecutor = (*Service)(nil)

// ChatCompletion executes one relay turn through enqueue and the durable
// result wait: fail fast when no relay worker is present, poll the single
// child job, and map the persisted terminal state for the provider.
func (s *Service) ChatCompletion(ctx context.Context, params annotator.RelayChatParams) (annotator.RelayChatOutcome, error) {
	if s == nil || s.db == nil || s.jobs == nil {
		return annotator.RelayChatOutcome{}, &annotator.Error{Code: annotator.CodeUnavailable, Err: errors.New("llmrelay: nil database")}
	}
	messages := make([]Message, 0, len(params.Messages))
	for _, message := range params.Messages {
		messages = append(messages, Message{Role: message.Role, Content: message.Content})
	}
	requestID := library.NewULID()
	payload, inputHash, err := BuildChatCompletion(requestID, params.Model, messages, params.OutputSchema, params.TemperatureMilli, params.MaxOutputTokens)
	if err != nil {
		return annotator.RelayChatOutcome{}, &annotator.Error{Code: annotator.CodeInvalidInput, Err: err}
	}
	if !s.Available(ctx) {
		return annotator.RelayChatOutcome{}, &annotator.Error{Code: annotator.CodeUnavailable, Err: errors.New("no relay-capable worker is present")}
	}
	job, err := s.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		return annotator.RelayChatOutcome{}, &annotator.Error{Code: annotator.CodeProviderFailure, Err: err}
	}
	stored, err := s.Wait(ctx, job.ID)
	if err != nil {
		return annotator.RelayChatOutcome{}, mapWaitError(ctx, err)
	}
	if stored.Operation != OperationChatCompletion {
		return annotator.RelayChatOutcome{}, &annotator.Error{Code: annotator.CodeProtocol, Err: fmt.Errorf("llmrelay unexpected operation %q", stored.Operation)}
	}
	result, err := DecodeChatResult([]byte(stored.ResultJSON), requestID.String(), MaxCompletionBytes)
	if err != nil {
		return annotator.RelayChatOutcome{}, &annotator.Error{Code: annotator.CodeInvalidOutput, Err: err}
	}
	return annotator.RelayChatOutcome{
		Text: result.Content, ReportedModel: result.ReportedModel,
		ProviderRequestID: result.ProviderRequestID, FinishReason: result.FinishReason,
		UsageJSON: result.UsageJSON, TimingJSON: result.TimingJSON,
		RelayJobID: job.ID.String(), RelayRequestID: requestID.String(), Model: params.Model,
	}, nil
}

// ListRelayModels executes one relay model catalog round trip. An empty
// model list is a valid transport result; the provider maps it to
// CodeUnavailable.
func (s *Service) ListRelayModels(ctx context.Context) ([]annotator.Model, error) {
	if s == nil || s.db == nil || s.jobs == nil {
		return nil, &annotator.Error{Code: annotator.CodeUnavailable, Err: errors.New("llmrelay: nil database")}
	}
	requestID := library.NewULID()
	payload, inputHash, err := BuildListModels(requestID)
	if err != nil {
		return nil, &annotator.Error{Code: annotator.CodeProtocol, Err: err}
	}
	if !s.Available(ctx) {
		return nil, &annotator.Error{Code: annotator.CodeUnavailable, Err: errors.New("no relay-capable worker is present")}
	}
	job, err := s.Enqueue(ctx, payload, inputHash, requestID.String())
	if err != nil {
		return nil, &annotator.Error{Code: annotator.CodeProviderFailure, Err: err}
	}
	stored, err := s.Wait(ctx, job.ID)
	if err != nil {
		return nil, mapWaitError(ctx, err)
	}
	if stored.Operation != OperationListModels {
		return nil, &annotator.Error{Code: annotator.CodeProtocol, Err: fmt.Errorf("llmrelay unexpected operation %q", stored.Operation)}
	}
	result, err := DecodeListModelsResult([]byte(stored.ResultJSON), requestID.String(), MaxCompletionBytes)
	if err != nil {
		return nil, &annotator.Error{Code: annotator.CodeInvalidOutput, Err: err}
	}
	models := make([]annotator.Model, 0, len(result.Models))
	for _, id := range result.Models {
		models = append(models, annotator.Model{ID: id, DisplayName: id})
	}
	return models, nil
}

// mapWaitError maps relay terminal states to provider errors. A done parent
// context always wins: cancellation is preserved so the pipeline's
// lease-loss path wins, and provider deadlines report timeout.
func mapWaitError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctx.Err() == context.Canceled {
			return &annotator.Error{Code: annotator.CodeProviderFailure, Err: ctx.Err()}
		}
		if ctx.Err() == context.DeadlineExceeded {
			return &annotator.Error{Code: annotator.CodeTimeout, Err: errors.New("provider request timed out")}
		}
	}
	var terminal *TerminalError
	if errors.As(err, &terminal) {
		switch terminal.Code {
		case CodeAuth:
			return &annotator.Error{Code: annotator.CodeNotAuthenticated, Err: errors.New("relay authentication failed")}
		case CodeInvalidResponse:
			return &annotator.Error{Code: annotator.CodeInvalidOutput, Err: errors.New("relay returned an invalid response")}
		case CodeUnreachable, CodeModelUnknown, jobs.LeaseExpiredErrorCode, CodeParentCanceled:
			return &annotator.Error{Code: annotator.CodeUnavailable, Err: fmt.Errorf("relay unavailable: %s", terminal.Code)}
		default:
			return &annotator.Error{Code: annotator.CodeProviderFailure, Err: fmt.Errorf("relay failed: %s", terminal.Code)}
		}
	}
	var relayErr *Error
	if errors.As(err, &relayErr) && relayErr.Code == CodeUnavailable {
		return &annotator.Error{Code: annotator.CodeUnavailable, Err: relayErr.Err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &annotator.Error{Code: annotator.CodeTimeout, Err: errors.New("provider request timed out")}
	}
	if errors.Is(err, context.Canceled) {
		return &annotator.Error{Code: annotator.CodeProviderFailure, Err: context.Canceled}
	}
	return &annotator.Error{Code: annotator.CodeProviderFailure, Err: err}
}
