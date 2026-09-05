package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doublangu/internal/library"
	"doublangu/internal/localworker"
	"doublangu/internal/media"
	"doublangu/internal/store"
	"doublangu/internal/workers"
)

func newAilocalsHandlerHarness(t *testing.T) *AilocalsHandler {
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
	return NewAilocalsHandler(service, "beta")
}

func ailocalsPost(t *testing.T, handler *AilocalsHandler, path string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.SetPathValue("id", strings.TrimPrefix(path, "/api/ailocals/v1/jobs/"))
	recorder := httptest.NewRecorder()
	switch path {
	case "/api/ailocals/v1/enroll":
		handler.ServeEnroll(recorder, request)
	case "/api/ailocals/v1/presence":
		handler.ServePresence(recorder, request)
	case "/api/ailocals/v1/lease":
		handler.ServeLease(recorder, request)
	default:
		if strings.Contains(path, "/heartbeat") {
			handler.ServeHeartbeat(recorder, request)
		} else if strings.Contains(path, "/complete") {
			handler.ServeComplete(recorder, request)
		} else if strings.Contains(path, "/fail") {
			handler.ServeFail(recorder, request)
		} else {
			t.Fatalf("unknown path %q", path)
		}
	}
	return recorder
}

func TestAilocalsInfoShape(t *testing.T) {
	handler := newAilocalsHandlerHarness(t)
	request := httptest.NewRequest(http.MethodGet, "/api/ailocals/v1/info", nil)
	recorder := httptest.NewRecorder()
	handler.ServeInfo(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("info status = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["protocol_version"] != localworker.ProtocolVersion || body["service_kind"] != "doublangu" || body["environment"] != "beta" {
		t.Fatalf("info body = %v", body)
	}
	capabilities, ok := body["supported_capabilities"].([]any)
	if !ok || len(capabilities) != 3 {
		t.Fatalf("supported capabilities = %v", body["supported_capabilities"])
	}
	limits, ok := body["limits"].(map[string]any)
	if !ok || limits["lease_seconds"].(float64) != 90 || limits["control_max_bytes"].(float64) != 262144 {
		t.Fatalf("limits = %v", limits)
	}
}

func TestAilocalsEnrollAndPresenceErrorEnvelopes(t *testing.T) {
	handler := newAilocalsHandlerHarness(t)

	// Missing enrollment header: 401 unauthorized envelope.
	response := ailocalsPost(t, handler, "/api/ailocals/v1/enroll", nil,
		[]byte(`{"protocol_version":"ailocals.v1","worker_name":"x","software_version":"1","capabilities":[]}`))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", response.Code)
	}
	var errBody map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody["protocol_version"] != localworker.ProtocolVersion {
		t.Fatalf("error envelope = %v", errBody)
	}

	// Invalid body: 400 invalid_request envelope.
	response = ailocalsPost(t, handler, "/api/ailocals/v1/enroll",
		map[string]string{localworker.EnrollmentTokenHeader: "token"},
		[]byte(`{"protocol_version":"speech-worker.v1"}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"code":"protocol_unsupported"`) {
		t.Fatalf("error body = %s", response.Body.String())
	}

	// Malformed JSON: 400.
	response = ailocalsPost(t, handler, "/api/ailocals/v1/enroll",
		map[string]string{localworker.EnrollmentTokenHeader: "token"},
		[]byte(`{invalid`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d", response.Code)
	}
}

func TestAilocalsWorkerRoutesRequireCommonCredential(t *testing.T) {
	handler := newAilocalsHandlerHarness(t)
	for _, path := range []string{"/api/ailocals/v1/presence", "/api/ailocals/v1/lease"} {
		response := ailocalsPost(t, handler, path, nil,
			[]byte(`{"protocol_version":"ailocals.v1"}`))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status = %d", path, response.Code)
		}
		if !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
			t.Fatalf("%s error body = %s", path, response.Body.String())
		}
	}
}

func TestAilocalsLeaseWithoutWorkIs204(t *testing.T) {
	handler := newAilocalsHandlerHarness(t)
	token := ailocalsEnrollViaHTTP(t, handler)

	response := ailocalsPost(t, handler, "/api/ailocals/v1/lease",
		map[string]string{localworker.WorkerTokenHeader: token},
		[]byte(`{"protocol_version":"ailocals.v1","capability_id":"tts.chatterbox.v3","wait_seconds":0}`))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("empty lease status = %d body = %q", response.Code, response.Body.String())
	}
}

func TestAilocalsCompleteRejectsBadMultipartAndHash(t *testing.T) {
	handler := newAilocalsHandlerHarness(t)
	token := ailocalsEnrollViaHTTP(t, handler)
	jobID := library.NewULID()
	leaseHeaders := map[string]string{
		localworker.WorkerTokenHeader: token,
		localworker.LeaseTokenHeader:  strings.Repeat("a", 43),
	}

	// Duplicate parts must be refused before publication.
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for i := 0; i < 2; i++ {
		part, _ := writer.CreateFormField("metadata")
		part.Write([]byte(`{"protocol_version":"ailocals.v1","attempt":1,"result_sha256":"` + strings.Repeat("a", 64) + `"}`))
	}
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/ailocals/v1/jobs/"+jobID.String()+"/complete", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for key, value := range leaseHeaders {
		request.Header.Set(key, value)
	}
	request.SetPathValue("id", jobID.String())
	recorder := httptest.NewRecorder()
	handler.ServeComplete(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate parts status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	// A result hash mismatch is refused before publication.
	completeBody := &bytes.Buffer{}
	writer = multipart.NewWriter(completeBody)
	metadataPart, _ := writer.CreateFormField("metadata")
	metadataPart.Write([]byte(`{"protocol_version":"ailocals.v1","attempt":1,"result_sha256":"` + strings.Repeat("b", 64) + `"}`))
	resultPart, _ := writer.CreateFormField("result")
	resultPart.Write([]byte(`{}`))
	writer.Close()
	request = httptest.NewRequest(http.MethodPost, "/api/ailocals/v1/jobs/"+jobID.String()+"/complete", completeBody)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for key, value := range leaseHeaders {
		request.Header.Set(key, value)
	}
	request.SetPathValue("id", jobID.String())
	recorder = httptest.NewRecorder()
	handler.ServeComplete(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("hash mismatch status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("hash mismatch body = %s", recorder.Body.String())
	}
}

func ailocalsEnrollViaHTTP(t *testing.T, handler *AilocalsHandler) string {
	t.Helper()
	enrollBody := []byte(`{
		"protocol_version":"ailocals.v1",
		"worker_name":"HTTP Mac",
		"software_version":"0.1.0",
		"capabilities":[{"id":"tts.chatterbox.v3","category":"tts","parameters":{
			"engine":"chatterbox","languages":["nl"],"unit_kinds":["sentence"],
			"max_bytes":2097152,"max_duration_ms":30000}}]
	}`)
	// An invalid enrollment token must be rejected; the strict body decode
	// still happens before the token use.
	response := ailocalsPost(t, handler, "/api/ailocals/v1/enroll",
		map[string]string{localworker.EnrollmentTokenHeader: strings.Repeat("z", 43)}, enrollBody)
	if response.Code != http.StatusUnauthorized && response.Code != http.StatusConflict {
		t.Fatalf("enroll status = %d body = %s", response.Code, response.Body.String())
	}
	// Create a real worker through the service so authenticated routes have
	// a credential to exercise the guard paths.
	enrollment, err := handler.service.CreateEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	response = ailocalsPost(t, handler, "/api/ailocals/v1/enroll",
		map[string]string{localworker.EnrollmentTokenHeader: enrollment.Token}, enrollBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("enroll with real token status = %d body = %s", response.Code, response.Body.String())
	}
	var created struct {
		ProtocolVersion string `json:"protocol_version"`
		WorkerID        string `json:"worker_id"`
		WorkerToken     string `json:"worker_token"`
		Environment     string `json:"environment"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ProtocolVersion != localworker.ProtocolVersion || created.WorkerToken == "" || created.Environment != "beta" {
		t.Fatalf("enroll response = %+v", created)
	}
	return created.WorkerToken
}
