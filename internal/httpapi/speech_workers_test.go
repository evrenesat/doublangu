package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/llmrelay"
	"doublangu/internal/media"
	"doublangu/internal/speech"
	"doublangu/internal/store"
	"doublangu/internal/workers"
)

func seedTTSRender(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	articleID := "01J0000000000000000000090"
	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, 'Worker test', 'nl', 'en', 'draft')`, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES ('01J0000000000000000000091', ?, 0, 'paragraph', 'Een zin.')`, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES ('01J0000000000000000000092', '01J0000000000000000000091', 0, 0, 8, 'Een zin.', 'source-hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, kind, role, shadow_policy, shadow_text, confidence_milli) VALUES ('01J0000000000000000000093', '01J0000000000000000000091', '01J0000000000000000000092', 'word', 'token', 'token', 'one', 900)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES ('01J0000000000000000000094', '01J0000000000000000000093', 0, 0, 3, 'Een')`); err != nil {
		t.Fatal(err)
	}
	if err := speech.NewStore(db).QueueArticleAudio(ctx, library.ULID(articleID), false); err != nil {
		t.Fatal(err)
	}
}

func relayHTTPFixture(t *testing.T) (*store.DB, *workers.Service, *SpeechWorkerHandler, *workers.Worker, string) {
	t.Helper()
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mediaStore, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := workers.NewService(db, mediaStore)
	handler := NewSpeechWorkerHandler(service, nil)
	ctx := context.Background()
	enrollment, err := service.CreateEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worker, token, err := service.Enroll(ctx, enrollment.Token, workers.EnrollInput{
		Name: "relay-mac", ProtocolVersion: speech.ProtocolVersion,
		Capabilities: []speech.WorkerCapability{{
			Engine: speech.ChatterboxEngine, Languages: []string{"nl"}, UnitKinds: []string{"sentence"},
			MaxBytes: 64 << 20, MaxDurationMS: 180000,
		}},
		LLMRelayCapabilities: []llmrelay.RelayCapability{{MaxCompletionBytes: llmrelay.RelayCapabilityBytes}},
		SoftwareVersion:      "0.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	authed, err := service.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	_ = worker
	return db, service, handler, authed, token
}

func seedHTTPRelayJob(t *testing.T, db *store.DB) (library.ULID, []byte) {
	t.Helper()
	relay := llmrelay.NewService(db)
	requestID := library.NewULID()
	payload, hash, err := llmrelay.BuildChatCompletion(requestID, "qwen-test",
		[]llmrelay.Message{{Role: "user", Content: "Vertaal: de bank"}},
		json.RawMessage(`{"type":"object"}`), 0, 16384)
	if err != nil {
		t.Fatal(err)
	}
	job, err := relay.Enqueue(context.Background(), payload, hash, requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	return job.ID, payload
}

func leaseHTTPRelay(t *testing.T, service *workers.Service, worker *workers.Worker) *workers.LeaseResponse {
	t.Helper()
	lease, err := service.Lease(context.Background(), worker, workers.LeaseRequest{
		ProtocolVersion:      speech.ProtocolVersion,
		LLMRelayCapabilities: []llmrelay.RelayCapability{{MaxCompletionBytes: llmrelay.RelayCapabilityBytes}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func relayResultBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	decoded, err := llmrelay.DecodeRelayPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{
		"request_id": decoded.RequestID, "content": `{"translation":"de bank"}`,
		"reported_model": "qwen-test", "provider_request_id": "chatcmpl-1",
		"finish_reason": "stop",
		"usage":         map[string]any{},
		"timing":        map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func completeRequest(t *testing.T, handler *SpeechWorkerHandler, jobID library.ULID, workerToken string, parts map[string][]byte, repeat map[string]int) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, ok := parts["metadata"]
	if ok {
		if err := writer.WriteField("metadata", string(metadata)); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < repeat["metadata"]; i++ {
			if err := writer.WriteField("metadata", string(metadata)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"audio", "result", "extra"} {
		value, ok := parts[name]
		if !ok {
			continue
		}
		count := repeat[name]
		if count < 1 {
			count = 1
		}
		for i := 0; i < count; i++ {
			part, err := writer.CreateFormFile(name, name+".bin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/speech-worker/jobs/"+jobID.String()+"/complete", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Doublangu-Worker-Token", workerToken)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/speech-worker/jobs/{id}/complete", handler.ServeComplete)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func relayMetadata(t *testing.T, lease *workers.LeaseResponse) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"protocol_version": speech.ProtocolVersion,
		"attempt":          lease.Attempt,
		"lease_token":      lease.LeaseToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestServeCompleteRelayMatrix(t *testing.T) {
	db, service, handler, worker, token := relayHTTPFixture(t)

	// Golden relay completion.
	_, payload := seedHTTPRelayJob(t, db)
	lease := leaseHTTPRelay(t, service, worker)
	recorder := completeRequest(t, handler, lease.JobID, token, map[string][]byte{
		"metadata": relayMetadata(t, lease),
		"result":   relayResultBytes(t, payload),
	}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("golden relay complete = %d: %s", recorder.Code, recorder.Body.String())
	}

	// Duplicate result parts are rejected.
	_, payload = seedHTTPRelayJob(t, db)
	lease = leaseHTTPRelay(t, service, worker)
	recorder = completeRequest(t, handler, lease.JobID, token, map[string][]byte{
		"metadata": relayMetadata(t, lease),
		"result":   relayResultBytes(t, payload),
	}, map[string]int{"result": 2})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate result = %d", recorder.Code)
	}

	// Unknown multipart names are rejected.
	_, payload = seedHTTPRelayJob(t, db)
	lease = leaseHTTPRelay(t, service, worker)
	recorder = completeRequest(t, handler, lease.JobID, token, map[string][]byte{
		"metadata": relayMetadata(t, lease),
		"result":   relayResultBytes(t, payload),
		"extra":    []byte("x"),
	}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown part = %d", recorder.Code)
	}

	// Missing metadata is rejected.
	_, payload = seedHTTPRelayJob(t, db)
	lease = leaseHTTPRelay(t, service, worker)
	recorder = completeRequest(t, handler, lease.JobID, token, map[string][]byte{
		"result": relayResultBytes(t, payload),
	}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing metadata = %d", recorder.Code)
	}

	// Oversized results are rejected with 413.
	_, _ = seedHTTPRelayJob(t, db)
	lease = leaseHTTPRelay(t, service, worker)
	recorder = completeRequest(t, handler, lease.JobID, token, map[string][]byte{
		"metadata": relayMetadata(t, lease),
		"result":   bytes.Repeat([]byte("x"), (2<<20)+1),
	}, nil)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized result = %d", recorder.Code)
	}

	// A wrong request echo fails relay validation with the relay code.
	_, _ = seedHTTPRelayJob(t, db)
	lease = leaseHTTPRelay(t, service, worker)
	wrong, _ := json.Marshal(map[string]any{"request_id": "other", "content": "x"})
	recorder = completeRequest(t, handler, lease.JobID, token, map[string][]byte{
		"metadata": relayMetadata(t, lease),
		"result":   wrong,
	}, nil)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong echo status = %d", recorder.Code)
	}
	var failure APIError
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != ErrCodeRelayUploadRejected {
		t.Fatalf("wrong echo code = %q", failure.Code)
	}

	// A TTS job carrying a relay result is rejected without touching audio.
	seedTTSRender(t, db)
	ttsLease, err := service.Lease(context.Background(), worker, workers.LeaseRequest{
		ProtocolVersion: speech.ProtocolVersion,
		Capabilities: []speech.WorkerCapability{{
			Engine: speech.ChatterboxEngine, Languages: []string{"nl"}, UnitKinds: []string{"sentence"},
			MaxBytes: 64 << 20, MaxDurationMS: 180000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ttsMetadata, _ := json.Marshal(map[string]any{
		"protocol_version": speech.ProtocolVersion,
		"attempt":          ttsLease.Attempt,
		"lease_token":      ttsLease.LeaseToken,
		"artifact": map[string]any{
			"request_hash": ttsLease.RequestHash, "sha256": strings.Repeat("a", 64),
			"size_bytes": 20, "mime_type": speech.AudioMIME, "codec": speech.AudioCodec,
			"sample_rate_hz": speech.AudioSampleRate, "channels": speech.AudioChannels, "duration_ms": 100,
		},
	})
	recorder = completeRequest(t, handler, ttsLease.JobID, token, map[string][]byte{
		"metadata": ttsMetadata,
		"result":   []byte(`{"request_id":"x"}`),
	}, nil)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tts with result = %d", recorder.Code)
	}
	var ttsFailure APIError
	if err := json.Unmarshal(recorder.Body.Bytes(), &ttsFailure); err != nil {
		t.Fatal(err)
	}
	if ttsFailure.Code != ErrCodeAudioUploadRejected {
		t.Fatalf("tts with result code = %q", ttsFailure.Code)
	}
}
