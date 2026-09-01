// Package annotator defines the provider boundary used by article enrichment.
package annotator

import (
	"context"
	"errors"
	"fmt"

	"doublangu/internal/reader"
)

// These aliases keep the domain and provider contracts on one type definition
// without making the reader package depend on a provider implementation.
type ArticleInput = reader.ArticleInput
type ArticleInputBlock = reader.ArticleInputBlock
type Candidate = reader.Candidate

// Annotator turns a pasted article into strict, occurrence-addressed learning
// candidates. Implementations must not mutate the input.
type Annotator interface {
	Annotate(ctx context.Context, input ArticleInput) ([]Candidate, error)
}

const (
	CodeUnavailable      = "v1.annotator_unavailable"
	CodeNotAuthenticated = "v1.enrichment_not_authenticated"
	CodeTimeout          = "v1.enrichment_timeout"
	CodeProtocol         = "v1.enrichment_protocol_error"
	CodeInvalidOutput    = "v1.enrichment_invalid_output"
	CodeProviderFailure  = "v1.enrichment_provider_failure"
	CodeInvalidInput     = "v1.enrichment_invalid_input"
)

// Error carries a stable public classification while retaining a local cause
// for logs and tests. HTTP handlers expose Code only.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the stable error code for an annotator failure.
func CodeOf(err error) string {
	var typed *Error
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	return CodeProviderFailure
}

// Disabled is the explicit opt-out provider. It lets the server start while
// returning a useful API error instead of silently producing untranslated text.
type Disabled struct{}

func (Disabled) Annotate(context.Context, ArticleInput) ([]Candidate, error) {
	return nil, &Error{Code: CodeUnavailable, Err: errors.New("article annotator is disabled")}
}
