package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"doublangu/internal/pipeline"
	"doublangu/internal/semantics"
)

// maxStageCorrectiveTurns preserves the existing correction bound for each
// stage: one initial turn plus at most two corrective turns in the same
// logical session.
const maxStageCorrectiveTurns = 2

// StageError is the typed executor failure. Phase distinguishes the local
// stage artifact validation from the final v3 merge validation so owner
// diagnostics can tell implementation bugs from provider output problems.
type StageError struct {
	Stage pipeline.StageID
	Phase string // "provider" | "stage_validation" | "final_validation"
	Code  string
	Err   error
}

func (e *StageError) Error() string {
	if e == nil || e.Err == nil {
		return fmt.Sprintf("%s %s", e.Stage, e.Phase)
	}
	return fmt.Sprintf("%s %s: %v", e.Stage, e.Phase, e.Err)
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StageTurnRecord is the durable owner-visible turn artifact for one stage.
type StageTurnRecord struct {
	TurnIndex          int
	TurnKind           string
	Prompt             string
	OutputSchema       string
	CompletedResponse  string
	ResponseHash       string
	ValidationError    string
	ProviderError      string
	CompletionMetadata string
	StartedAt          string
	CompletedAt        string
	DurationMS         int64
	Status             string
}

// StageAttemptResult is the accumulated record of one stage execution.
type StageAttemptResult struct {
	Turns         []StageTurnRecord
	ReportedModel string
	RequestID     string
	FinishReason  string
	UsageJSON     string
	TimingJSON    string
	MetadataJSON  string
	StderrExcerpt string
}

type stageAdapter interface {
	StageID() pipeline.StageID
	Prompt() string
	OutputSchema() json.RawMessage
	Validate(raw string) error
}

// executeStage runs one stage through a session with at most two corrective
// turns. It always records every turn artifact, including provider failures,
// and returns the raw text of the first validated artifact.
func executeStage(ctx context.Context, provider Provider, binding ResolvedBinding, adapter stageAdapter) (string, StageAttemptResult, error) {
	session, err := provider.OpenSession(ctx, binding)
	if err != nil {
		return "", StageAttemptResult{}, &StageError{Stage: binding.StageID, Phase: "provider", Code: codeOf(err), Err: err}
	}
	defer session.Close()
	result := StageAttemptResult{}
	raw := ""
	var validationErr error
	correctiveUsed := 0
	for {
		prompt := adapter.Prompt()
		if correctiveUsed > 0 {
			prompt = BuildStageCorrectionPrompt(validationErr.Error(), raw)
		}
		startedAt := time.Now().UTC()
		started := startedAt.Format(time.RFC3339Nano)
		completedAt := started
		record := StageTurnRecord{
			TurnIndex: correctiveUsed, TurnKind: "initial", Prompt: prompt,
			OutputSchema: string(adapter.OutputSchema()), StartedAt: started, Status: "completed",
		}
		if correctiveUsed > 0 {
			record.TurnKind = "corrective"
		}
		turnRequest := TurnRequest{StageID: binding.StageID, Prompt: prompt, OutputSchema: adapter.OutputSchema()}
		completion, turnErr := session.Turn(ctx, turnRequest)
		completedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.CompletedAt = completedAt
		record.DurationMS = time.Since(startedAt).Milliseconds()
		result.ReportedModel = completion.ReportedModel
		result.RequestID = completion.RequestID
		result.FinishReason = completion.FinishReason
		// Usage and timing accumulate across every completion of the stage:
		// when an invalid initial response is followed by corrective turns,
		// the attempt records the summed provider work instead of only the
		// final request. Per-turn completion metadata is already retained on
		// the individual turn records.
		result.UsageJSON = accumulateCompletionNumbers(result.UsageJSON, completion.UsageJSON)
		result.TimingJSON = accumulateCompletionNumbers(result.TimingJSON, completion.TimingJSON)
		result.MetadataJSON = completion.ProviderMetadataJSON
		result.StderrExcerpt = completion.StderrExcerpt
		if turnErr != nil {
			record.ProviderError = turnErr.Error()
			record.Status = "failed"
			result.Turns = append(result.Turns, record)
			return "", result, &StageError{Stage: binding.StageID, Phase: "provider", Code: codeOf(turnErr), Err: turnErr}
		}
		record.CompletedResponse = completion.Text
		record.ResponseHash = hashText(completion.Text)
		record.CompletionMetadata = completion.ProviderMetadataJSON
		result.Turns = append(result.Turns, record)
		if err := adapter.Validate(completion.Text); err != nil {
			validationErr = err
			// Preserve the rejected artifact so the corrective prompt can
			// show the provider exactly what was invalid.
			raw = completion.Text
			record.ValidationError = err.Error()
			result.Turns[len(result.Turns)-1] = record
			if correctiveUsed >= maxStageCorrectiveTurns {
				return "", result, &StageError{Stage: binding.StageID, Phase: "stage_validation", Code: CodeInvalidOutput, Err: err}
			}
			correctiveUsed++
			continue
		}
		validationErr = nil
		raw = completion.Text
		break
	}
	if strings.TrimSpace(raw) == "" {
		return "", result, &StageError{Stage: binding.StageID, Phase: "stage_validation", Code: CodeInvalidOutput, Err: errors.New("stage produced no validated artifact")}
	}
	return raw, result, nil
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) {
		if typed.Code != "" {
			return typed.Code
		}
	}
	return CodeProviderFailure
}

// accumulateCompletionNumbers merges two canonical JSON diagnostic objects
// (usage and timing totals) by summing every numeric leaf value, so an
// attempt spanning several corrective turns reports the full provider work.
// Non-numeric leaves are carried over from the accumulated object; malformed
// input leaves the accumulator unchanged.
func accumulateCompletionNumbers(accumulated, next string) string {
	if strings.TrimSpace(next) == "" {
		return accumulated
	}
	if strings.TrimSpace(accumulated) == "" {
		return next
	}
	var base, addition map[string]any
	if err := json.Unmarshal([]byte(accumulated), &base); err != nil {
		return accumulated
	}
	if err := json.Unmarshal([]byte(next), &addition); err != nil {
		return accumulated
	}
	for key, value := range addition {
		added, ok := jsonNumber(value)
		if !ok {
			continue
		}
		if existing, ok := jsonNumber(base[key]); ok {
			base[key] = existing + added
		} else {
			base[key] = added
		}
	}
	merged, err := json.Marshal(base)
	if err != nil {
		return accumulated
	}
	return string(merged)
}

// jsonNumber returns the numeric value of a decoded JSON leaf.
func jsonNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}

// ---------------------------------------------------------------------------
// Linguistic adapter

type linguisticStageAdapter struct {
	chunk semantics.PreparedChunk
}

func (a *linguisticStageAdapter) StageID() pipeline.StageID { return pipeline.StageLinguisticAnalysis }

func (a *linguisticStageAdapter) Prompt() string { return BuildLinguisticChunkPrompt(a.chunk) }

func (a *linguisticStageAdapter) OutputSchema() json.RawMessage {
	raw, _ := json.Marshal(LinguisticOutputSchema(a.chunk))
	return raw
}

func (a *linguisticStageAdapter) Validate(raw string) error {
	artifact, err := semantics.DecodeLinguisticArtifact([]byte(raw))
	if err != nil {
		return err
	}
	_, err = semantics.ValidateLinguistic(a.chunk, artifact)
	return err
}

// ExecuteLinguisticStage runs the linguistic stage for one paragraph and
// returns the validated artifact.
func ExecuteLinguisticStage(ctx context.Context, provider Provider, binding ResolvedBinding, chunk semantics.PreparedChunk) (*semantics.ValidatedLinguistic, StageAttemptResult, error) {
	adapter := &linguisticStageAdapter{chunk: chunk}
	raw, result, err := executeStage(ctx, provider, binding, adapter)
	if err != nil {
		return nil, result, err
	}
	artifact, decodeErr := semantics.DecodeLinguisticArtifact([]byte(raw))
	if decodeErr != nil {
		return nil, result, &StageError{Stage: pipeline.StageLinguisticAnalysis, Phase: "stage_validation", Code: CodeInvalidOutput, Err: decodeErr}
	}
	validated, validateErr := semantics.ValidateLinguistic(chunk, artifact)
	if validateErr != nil {
		return nil, result, &StageError{Stage: pipeline.StageLinguisticAnalysis, Phase: "stage_validation", Code: CodeInvalidOutput, Err: validateErr}
	}
	return validated, result, nil
}

// ---------------------------------------------------------------------------
// Translation adapter

type translationStageAdapter struct {
	chunk      semantics.PreparedChunk
	linguistic *semantics.ValidatedLinguistic
}

func (a *translationStageAdapter) StageID() pipeline.StageID { return pipeline.StageTranslation }

func (a *translationStageAdapter) Prompt() string {
	return BuildTranslationChunkPrompt(a.chunk, a.linguistic)
}

func (a *translationStageAdapter) OutputSchema() json.RawMessage {
	raw, _ := json.Marshal(TranslationOutputSchema(a.chunk, a.linguistic))
	return raw
}

func (a *translationStageAdapter) Validate(raw string) error {
	artifact, err := semantics.DecodeTranslationArtifact([]byte(raw))
	if err != nil {
		return err
	}
	return semantics.ValidateTranslation(a.chunk, a.linguistic, artifact)
}

// TranslationStageOutput is the validated translation plus the merged v3
// response that still has to pass the unchanged chunk validator.
type TranslationStageOutput struct {
	Artifact semantics.TranslationArtifact
	Merged   semantics.Response
}

// ExecuteTranslationStage runs the translation stage and merges both
// artifacts. Merge or final-validation failures surface as a
// final_validation phase error on the translation stage.
func ExecuteTranslationStage(ctx context.Context, provider Provider, binding ResolvedBinding, chunk semantics.PreparedChunk, linguistic *semantics.ValidatedLinguistic) (*TranslationStageOutput, StageAttemptResult, error) {
	adapter := &translationStageAdapter{chunk: chunk, linguistic: linguistic}
	raw, result, err := executeStage(ctx, provider, binding, adapter)
	if err != nil {
		return nil, result, err
	}
	artifact, decodeErr := semantics.DecodeTranslationArtifact([]byte(raw))
	if decodeErr != nil {
		return nil, result, &StageError{Stage: pipeline.StageTranslation, Phase: "final_validation", Code: CodeInvalidOutput, Err: decodeErr}
	}
	if validateErr := semantics.ValidateTranslation(chunk, linguistic, artifact); validateErr != nil {
		return nil, result, &StageError{Stage: pipeline.StageTranslation, Phase: "stage_validation", Code: CodeInvalidOutput, Err: validateErr}
	}
	merged, mergeErr := semantics.MergeLinguisticTranslation(linguistic, artifact)
	if mergeErr != nil {
		return nil, result, &StageError{Stage: pipeline.StageTranslation, Phase: "final_validation", Code: CodeInvalidOutput, Err: mergeErr}
	}
	if _, validateErr := semantics.ValidateChunkResponse(chunk, merged); validateErr != nil {
		return nil, result, &StageError{Stage: pipeline.StageTranslation, Phase: "final_validation", Code: CodeInvalidOutput, Err: validateErr}
	}
	return &TranslationStageOutput{Artifact: artifact, Merged: merged}, result, nil
}

// StageErrorCode extracts the stable annotator code from a StageError.
func StageErrorCode(err error) string {
	var stageErr *StageError
	if errors.As(err, &stageErr) && stageErr.Code != "" {
		return stageErr.Code
	}
	return codeOf(err)
}

// StageErrorPhase returns the phase marker for owner diagnostics.
func StageErrorPhase(err error) string {
	var stageErr *StageError
	if errors.As(err, &stageErr) {
		return strings.TrimSpace(string(stageErr.Stage) + " " + stageErr.Phase)
	}
	return "provider"
}
