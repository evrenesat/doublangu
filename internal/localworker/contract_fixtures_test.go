package localworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contractDir locates the vendored frozen contract when tests run from the
// repository.
func contractDir(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{
		filepath.Join("..", "..", "contracts", "ailocals-v1"),
		filepath.Join("contracts", "ailocals-v1"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	t.Skip("vendored contract not found")
	return ""
}

type fixtureCase struct {
	ID             string `json:"id"`
	Operation      string `json:"operation"`
	Direction      string `json:"direction"`
	Valid          bool   `json:"valid"`
	CapabilityKind string `json:"capability"`
	Files          []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
	ExpectedStatus int `json:"expected_status"`
}

func manifestCases(t *testing.T, root string) []fixtureCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "fixtures", "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var manifest struct {
		Contract string        `json:"contract"`
		Cases    []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	if manifest.Contract != ProtocolVersion {
		t.Fatalf("manifest contract = %q", manifest.Contract)
	}
	return manifest.Cases
}

func caseBytes(t *testing.T, root string, item fixtureCase) []byte {
	t.Helper()
	if len(item.Files) != 1 {
		t.Skipf("case %s has %d files", item.ID, len(item.Files))
	}
	raw, err := os.ReadFile(filepath.Join(root, "fixtures", item.Files[0].Path))
	if err != nil {
		t.Fatalf("fixture %s: %v", item.ID, err)
	}
	sum := SHA256Hex(raw)
	if sum != item.Files[0].SHA256 {
		t.Fatalf("fixture %s hash mismatch: %s != %s", item.ID, sum, item.Files[0].SHA256)
	}
	return raw
}

// TestVendoredContractEnrollFixtures decodes the shared enrollment fixtures
// with the production decoders so consumer behavior stays byte-grounded.
func TestVendoredContractEnrollFixtures(t *testing.T) {
	root := contractDir(t)
	for _, item := range manifestCases(t, root) {
		if item.Operation != "enroll" || item.Direction != "request" {
			continue
		}
		raw := caseBytes(t, root, item)
		var request EnrollRequest
		decodeErr := DecodeStrictJSON(raw, &request)
		validateErr := ValidateEnrollRequest(&request)
		if item.Valid {
			if decodeErr != nil || validateErr != nil {
				t.Fatalf("valid case %s rejected: decode=%v validate=%v", item.ID, decodeErr, validateErr)
			}
			continue
		}
		if decodeErr == nil && validateErr == nil {
			t.Fatalf("invalid case %s accepted", item.ID)
		}
	}
}

// TestVendoredContractPayloadFixtures validates the TTS and relay payload
// fixtures against the mapping layer.
func TestVendoredContractPayloadFixtures(t *testing.T) {
	root := contractDir(t)
	for _, item := range manifestCases(t, root) {
		if item.Operation != "payload" || item.Direction != "request" || !item.Valid {
			continue
		}
		raw := caseBytes(t, root, item)
		switch {
		case strings.Contains(item.ID, "tts"):
			var payload TtsPayload
			if err := strictUnmarshalForTest(raw, &payload); err != nil {
				t.Fatalf("tts fixture %s: %v", item.ID, err)
			}
			if payload.RequestHash == "" || payload.Profile.Engine == "" {
				t.Fatalf("tts fixture %s missing domain fields: %+v", item.ID, payload)
			}
		case strings.Contains(item.ID, "relay"):
			envelope, err := DecodeRelayPayload(raw)
			if err != nil {
				t.Fatalf("relay fixture %s: %v", item.ID, err)
			}
			if !RelayOperations[envelope.Operation] {
				t.Fatalf("relay fixture %s has invalid operation", item.ID)
			}
		}
	}
}

func strictUnmarshalForTest(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// TestVendoredContractInvalidEnrollRejected pins a handful of invalid
// enrollment fixtures to their expected failure.
func TestVendoredContractInvalidEnrollRejected(t *testing.T) {
	root := contractDir(t)
	rejected := 0
	for _, item := range manifestCases(t, root) {
		if item.Operation != "enroll" || item.Direction != "request" || item.Valid {
			continue
		}
		raw := caseBytes(t, root, item)
		var request EnrollRequest
		if err := DecodeStrictJSON(raw, &request); err != nil {
			rejected++
			continue
		}
		if err := ValidateEnrollRequest(&request); err != nil {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected invalid enroll fixtures in the manifest")
	}
}
