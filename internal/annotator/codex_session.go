package annotator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"doublangu/internal/config"
)

// codexStageProvider is the local authenticated Codex app-server transport
// for one configured provider entry.
type codexStageProvider struct {
	descriptor ProviderDescriptor
	binary     string
	timeout    time.Duration
}

func (p *codexStageProvider) Descriptor() ProviderDescriptor { return p.descriptor }

func (p *codexStageProvider) ListModels(ctx context.Context) ([]Model, error) {
	// The catalog path is process-scoped and independent of model/effort, so
	// reuse the existing single-process listing logic through a temporary
	// adapter carrying this provider's binary and timeout.
	adapter := NewCodexAppServer(CodexConfig{Binary: p.binary, Timeout: p.timeout})
	return adapter.ListModels(ctx)
}

func (p *codexStageProvider) OpenSession(ctx context.Context, binding ResolvedBinding) (Session, error) {
	if binding.ProviderType != ProviderTypeCodexAppServer {
		return nil, fmt.Errorf("provider %q is not a codex app-server", binding.ProviderID)
	}
	var options config.CodexOptions
	if err := json.Unmarshal(binding.Options, &options); err != nil {
		return nil, fmt.Errorf("decode codex options: %w", err)
	}
	if options.ReasoningEffort == "" {
		return nil, errors.New("codex options require reasoning_effort")
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultCodexAnalysisTimeout
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	workingDirectory, err := os.MkdirTemp("", "doublangu-codex-session-")
	if err != nil {
		cancel()
		return nil, &Error{Code: CodeUnavailable, Err: fmt.Errorf("create private app-server directory: %w", err)}
	}
	process, err := launchAppServer(runContext, p.binary, workingDirectory)
	if err != nil {
		cancel()
		_ = os.RemoveAll(workingDirectory)
		return nil, classifyCodex(runContext, nil, err, CodeUnavailable)
	}
	protocol := newProtocolClient(process.stdin, process.stdout)
	nextID := int64(1)
	if err := protocol.call(runContext, nextID, "initialize", initializeParams{
		ClientInfo:   initializeClientInfo{Name: "doublangu", Version: "0.1.0"},
		Capabilities: &initializeCapabilities{ExperimentalAPI: true},
	}, &map[string]any{}); err != nil {
		process.close()
		cancel()
		_ = os.RemoveAll(workingDirectory)
		return nil, classifyCodex(runContext, process, err, CodeProtocol)
	}
	nextID++
	var threadResponse threadStartResponse
	if err := protocol.call(runContext, nextID, "thread/start", threadStartParams{
		ApprovalPolicy: "never", Sandbox: "read-only", CWD: workingDirectory,
		Ephemeral: true, DynamicTools: []any{}, Model: binding.ModelID,
	}, &threadResponse); err != nil {
		process.close()
		cancel()
		_ = os.RemoveAll(workingDirectory)
		return nil, classifyCodex(runContext, process, err, CodeProtocol)
	}
	threadID := threadResponse.Thread.ID
	if threadID == "" {
		process.close()
		cancel()
		_ = os.RemoveAll(workingDirectory)
		return nil, &Error{Code: CodeProtocol, Err: errors.New("thread/start returned no thread id")}
	}
	return &codexStageSession{
		provider: p, binding: binding, effort: options.ReasoningEffort,
		runContext: runContext, cancel: cancel, workingDirectory: workingDirectory,
		process: process, protocol: protocol, threadID: threadID, nextID: nextID + 1,
	}, nil
}

type codexStageSession struct {
	provider         *codexStageProvider
	binding          ResolvedBinding
	effort           string
	runContext       context.Context
	cancel           context.CancelFunc
	workingDirectory string
	process          *appServerProcess
	protocol         *protocolClient
	threadID         string
	nextID           int64
	closed           bool
}

func (s *codexStageSession) Turn(ctx context.Context, request TurnRequest) (Completion, error) {
	if s == nil || s.closed {
		return Completion{}, &Error{Code: CodeUnavailable, Err: errors.New("codex session is closed")}
	}
	select {
	case <-ctx.Done():
		return Completion{}, classifyCodex(ctx, s.process, ctx.Err(), CodeTimeout)
	default:
	}
	result, err := s.protocol.runTurnDetailed(s.runContext, s.nextID, s.threadID, request.Prompt, s.effort, s.binding.ModelID, request.OutputSchema)
	s.nextID++
	if err != nil {
		if s.process != nil {
			s.process.close()
		}
		s.closed = true
		return Completion{}, classifyCodex(s.runContext, s.process, err, CodeProviderFailure)
	}
	return Completion{
		Text: result.Text, ReportedModel: result.ReportedModel,
		ProviderMetadataJSON: result.MetadataJSON,
		StderrExcerpt:        processStderr(s.process),
	}, nil
}

func (s *codexStageSession) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.process != nil {
		s.process.close()
	}
	s.cancel()
	return os.RemoveAll(s.workingDirectory)
}

func classifyCodex(ctx context.Context, process *appServerProcess, err error, fallback string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Err: errors.New("Codex app-server request timed out")}
	}
	if process != nil && process.stderr != nil && hasAuthenticationFailure(process.stderr.String()) {
		return &Error{Code: CodeNotAuthenticated, Err: errors.New("Codex is not authenticated")}
	}
	if typed := new(Error); errors.As(err, &typed) {
		return typed
	}
	return &Error{Code: fallback, Err: err}
}
