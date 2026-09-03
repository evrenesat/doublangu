package llmrelay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/jobs"
	"doublangu/internal/store"
)

func adapterTestDB(t *testing.T) (*store.DB, *Service) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, NewService(db)
}

func adapterPresence(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO speech_worker (id, name, protocol_version, token_hash, llm_relay_capabilities_json, relay_last_seen_at) VALUES ('01J00000000000000000000W1', 'mac', 'speech-worker.v1', 'tok', '[{"max_completion_bytes":2097152}]', ?)`, store.NowUTC()); err != nil {
		t.Fatal(err)
	}
}

// scriptedWorker claims the next relay job and settles it: fail with the
// given code, or complete with a result built by buildResult. It polls like
// a real long-polling worker because the job may not exist yet.
func scriptedWorker(t *testing.T, svc *Service, db *store.DB, failCode string, failRetry bool, buildResult func(requestID string) []byte, done chan<- error) {
	t.Helper()
	go func() {
		done <- serveClaims(svc, db, failCode, failRetry, buildResult)
	}()
}

// serveClaims serves relay claims until the job settles: completions finish
// it, a retryable failure is served again terminally on its retry, and any
// other failure ends it.
func serveClaims(svc *Service, db *store.DB, failCode string, failRetry bool, buildResult func(requestID string) []byte) error {
	ctx := context.Background()
	retry := failRetry
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		lease, err := svc.jobs.ClaimMatching(ctx, jobs.TargetMacOS, "01J00000000000000000000W1", nil)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if failCode != "" {
			if err := svc.jobs.Fail(ctx, lease.ID, lease.AttemptCount, lease.LeaseToken, failCode, retry); err != nil {
				return err
			}
			if !retry {
				return nil
			}
			retry = false
			continue
		}
		payload, err := DecodeRelayPayload([]byte(lease.PayloadJSON))
		if err != nil {
			return err
		}
		job, err := svc.jobs.Get(ctx, lease.ID)
		if err != nil {
			return err
		}
		return db.WithTransaction(ctx, func(tx *sql.Tx) error {
			return svc.CompleteTx(ctx, tx, *job, lease.AttemptCount, lease.LeaseToken, buildResult(payload.RequestID))
		})
	}
	return errors.New("scripted worker never settled a relay job")
}

func chatResultFor(requestID string) []byte {
	encoded, _ := json.Marshal(map[string]any{
		"request_id": requestID, "content": `{"translation":"de bank"}`,
		"reported_model": "qwen-test", "provider_request_id": "chatcmpl-9",
		"finish_reason": "stop",
		"usage":         map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		"timing":        map[string]any{"generation_duration": 0.5},
	})
	return encoded
}

func chatParams() annotator.RelayChatParams {
	return annotator.RelayChatParams{
		Model:            "qwen-test",
		Messages:         []annotator.RelayMessage{{Role: "user", Content: "Vertaal."}},
		OutputSchema:     json.RawMessage(`{"type":"object"}`),
		TemperatureMilli: 0, MaxOutputTokens: 16384,
	}
}

func TestAdapterChatCompletionSuccess(t *testing.T) {
	db, svc := adapterTestDB(t)
	adapterPresence(t, db)
	done := make(chan error, 1)
	scriptedWorker(t, svc, db, "", false, chatResultFor, done)
	outcome, err := svc.ChatCompletion(context.Background(), chatParams())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if outcome.Text != `{"translation":"de bank"}` || outcome.ReportedModel != "qwen-test" ||
		outcome.ProviderRequestID != "chatcmpl-9" || outcome.RelayJobID == "" ||
		outcome.RelayRequestID == "" || outcome.Model != "qwen-test" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestAdapterChatCompletionMapping(t *testing.T) {
	cases := []struct {
		name      string
		failCode  string
		failRetry bool
		code      string
	}{
		{"auth", CodeAuth, false, annotator.CodeNotAuthenticated},
		{"invalid", CodeInvalidResponse, false, annotator.CodeInvalidOutput},
		{"unreachable", CodeUnreachable, true, annotator.CodeUnavailable},
		{"model unknown", CodeModelUnknown, false, annotator.CodeUnavailable},
		{"canceled", CodeCanceled, false, annotator.CodeUnavailable},
	}
	for _, tc := range cases {
		db, svc := adapterTestDB(t)
		adapterPresence(t, db)
		done := make(chan error, 1)
		scriptedWorker(t, svc, db, tc.failCode, tc.failRetry, chatResultFor, done)
		if _, err := svc.ChatCompletion(context.Background(), chatParams()); annotator.CodeOf(err) != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.name, annotator.CodeOf(err), tc.code)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdapterOfflineFailFast(t *testing.T) {
	_, svc := adapterTestDB(t)
	if _, err := svc.ChatCompletion(context.Background(), chatParams()); annotator.CodeOf(err) != annotator.CodeUnavailable {
		t.Fatalf("offline code = %q", annotator.CodeOf(err))
	}
	if _, err := svc.ListRelayModels(context.Background()); annotator.CodeOf(err) != annotator.CodeUnavailable {
		t.Fatalf("offline catalog code = %q", annotator.CodeOf(err))
	}
}

func TestAdapterDeadlineAndCancel(t *testing.T) {
	db, svc := adapterTestDB(t)
	adapterPresence(t, db)
	deadline, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := svc.ChatCompletion(deadline, chatParams()); annotator.CodeOf(err) != annotator.CodeTimeout {
		t.Fatalf("deadline code = %q err=%v", annotator.CodeOf(err), err)
	}
	cancelable, stop := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		stop()
	}()
	if _, err := svc.ChatCompletion(cancelable, chatParams()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel must be preserved, got %v", err)
	}
}

func TestAdapterListRelayModels(t *testing.T) {
	db, svc := adapterTestDB(t)
	adapterPresence(t, db)
	done := make(chan error, 1)
	scriptedWorker(t, svc, db, "", false, func(requestID string) []byte {
		encoded, _ := json.Marshal(map[string]any{"request_id": requestID, "models": []string{"qwen-a", "qwen-b"}})
		return encoded
	}, done)
	models, err := svc.ListRelayModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "qwen-a" {
		t.Fatalf("models = %+v", models)
	}
	// An empty model list is a valid transport result.
	db2, svc2 := adapterTestDB(t)
	adapterPresence(t, db2)
	done2 := make(chan error, 1)
	scriptedWorker(t, svc2, db2, "", false, func(requestID string) []byte {
		encoded, _ := json.Marshal(map[string]any{"request_id": requestID, "models": []string{}})
		return encoded
	}, done2)
	empty, err := svc2.ListRelayModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty catalog = %+v", empty)
	}
}
