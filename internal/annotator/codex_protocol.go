package annotator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	maxProtocolLineBytes = 1 << 20
	maxProtocolBytes     = 8 << 20
	maxStderrBytes       = 16 << 10
)

type jsonRPCRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type jsonRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *jsonRPCError   `json:"error"`
}

type jsonRPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type initializeParams struct {
	ClientInfo   initializeClientInfo    `json:"clientInfo"`
	Capabilities *initializeCapabilities `json:"capabilities,omitempty"`
}

type initializeClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi"`
}

type threadStartParams struct {
	ApprovalPolicy string `json:"approvalPolicy"`
	Sandbox        string `json:"sandbox"`
	CWD            string `json:"cwd"`
	Ephemeral      bool   `json:"ephemeral"`
	DynamicTools   []any  `json:"dynamicTools"`
	Model          string `json:"model,omitempty"`
}

type threadStartResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type textInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type turnStartParams struct {
	ThreadID     string          `json:"threadId"`
	Input        []textInput     `json:"input"`
	Effort       string          `json:"effort,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Model        string          `json:"model,omitempty"`
}

type turnStartResponse struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type itemCompletedParams struct {
	CompletedAtMs int64 `json:"completedAtMs"`
	Item          struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type turnCompletedParams struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
}

type boundedBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data.Len() < b.limit {
		remaining := b.limit - b.data.Len()
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.data.Write(value)
	}
	return originalLength, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type appServerProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *boundedBuffer
}

func startAppServer(ctx context.Context, binary, workingDirectory string) (*appServerProcess, error) {
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	cmd.Dir = workingDirectory
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Err: fmt.Errorf("app-server stdout: %w", err)}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, &Error{Code: CodeUnavailable, Err: fmt.Errorf("app-server stdin: %w", err)}
	}
	stderr := &boundedBuffer{limit: maxStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdin.Close()
		if errors.Is(err, exec.ErrNotFound) {
			return nil, &Error{Code: CodeUnavailable, Err: errors.New("codex binary was not found")}
		}
		return nil, &Error{Code: CodeUnavailable, Err: fmt.Errorf("start app-server: %w", err)}
	}
	return &appServerProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *appServerProcess) close() {
	if p == nil {
		return
	}
	_ = p.stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	_ = p.stdout.Close()
}

type protocolClient struct {
	stdin io.Writer
	lines *bufio.Reader
	total int
}

func newProtocolClient(stdin io.Writer, stdout io.Reader) *protocolClient {
	return &protocolClient{stdin: stdin, lines: bufio.NewReaderSize(stdout, 32<<10)}
}

func (p *protocolClient) send(id int64, method string, params any) error {
	request, err := json.Marshal(jsonRPCRequest{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	request = append(request, '\n')
	if _, err := p.stdin.Write(request); err != nil {
		return fmt.Errorf("write %s: %w", method, err)
	}
	return nil
}

func (p *protocolClient) next(ctx context.Context) (jsonRPCMessage, error) {
	line, err := readProtocolLine(p.lines, maxProtocolLineBytes)
	if err != nil {
		return jsonRPCMessage{}, err
	}
	p.total += len(line)
	if p.total > maxProtocolBytes {
		return jsonRPCMessage{}, errors.New("app-server output exceeded the response limit")
	}
	var message jsonRPCMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return jsonRPCMessage{}, fmt.Errorf("decode app-server message: %w", err)
	}
	return message, nil
}

func readProtocolLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, isPrefix, err := reader.ReadLine()
		if len(part) > 0 {
			if len(line)+len(part) > limit {
				return nil, errors.New("app-server message exceeded the line limit")
			}
			line = append(line, part...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		if !isPrefix {
			return line, nil
		}
	}
}

func responseFor(message jsonRPCMessage, id int64) bool {
	if len(message.ID) == 0 {
		return false
	}
	var got int64
	return json.Unmarshal(message.ID, &got) == nil && got == id
}

func decodeResult(message jsonRPCMessage, target any) error {
	if message.Error != nil {
		return fmt.Errorf("app-server RPC %d: %s", message.Error.Code, message.Error.Message)
	}
	if len(message.Result) == 0 || string(message.Result) == "null" {
		return errors.New("app-server response has no result")
	}
	if err := json.Unmarshal(message.Result, target); err != nil {
		return fmt.Errorf("decode app-server result: %w", err)
	}
	return nil
}

func protocolError(err error) error {
	if errors.Is(err, io.EOF) {
		return protocolFailure(errors.New("app-server closed the protocol stream"))
	}
	return protocolFailure(err)
}

func hasAuthenticationFailure(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "not logged in") || strings.Contains(lower, "login required") || strings.Contains(lower, "authentication required")
}
