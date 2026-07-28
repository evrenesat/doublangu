package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// These compile-time assignments make a signature change fail the contract
// package build, before any runtime test can pass by only matching a method name.
var (
	_ func(Plugin) Manifest                                                            = Plugin.Manifest
	_ func(Plugin, context.Context, Host) error                                        = Plugin.Register
	_ func(Host) Settings                                                              = Host.Settings
	_ func(Host) Library                                                               = Host.Library
	_ func(Host) BlobStore                                                             = Host.Blobs
	_ func(Host) Logger                                                                = Host.Logger
	_ func(Host) *http.Client                                                          = Host.HTTPClient
	_ func(Host) EventBus                                                              = Host.EventBus
	_ func(Host, ProviderRegistration) error                                           = Host.RegisterProvider
	_ func(Host, TransformerRegistration) error                                        = Host.RegisterTransformer
	_ func(Host, ValidatorRegistration) error                                          = Host.RegisterValidator
	_ func(Host, ObserverRegistration) error                                           = Host.RegisterObserver
	_ func(Host, JobHandlerRegistration) error                                         = Host.RegisterJobHandler
	_ func(Host, EventHandlerRegistration) error                                       = Host.RegisterEventHandler
	_ func(Host, CommandRegistration) error                                            = Host.RegisterCommand
	_ func(Host, UIRegistration) error                                                 = Host.RegisterUI
	_ func(Settings, string) (ImmutableBytes, error)                                   = Settings.Get
	_ func(Settings, string, []byte) error                                             = Settings.Set
	_ func(Settings, string) error                                                     = Settings.Delete
	_ func(Settings) ([]string, error)                                                 = Settings.List
	_ func(Library, context.Context, *LibraryFilter, int, int) ([]LibraryTitle, error) = Library.ListTitles
	_ func(Library, context.Context, string) (*LibraryTitle, error)                    = Library.GetTitle
	_ func(BlobStore, context.Context, []byte) (string, error)                         = BlobStore.Put
	_ func(BlobStore, context.Context, string) (ImmutableBytes, error)                 = BlobStore.Get
	_ func(BlobStore, context.Context, string) (bool, error)                           = BlobStore.Exists
	_ func(Logger, string, ...interface{})                                             = Logger.Debug
	_ func(Logger, string, ...interface{})                                             = Logger.Info
	_ func(Logger, string, ...interface{})                                             = Logger.Warn
	_ func(Logger, string, ...interface{})                                             = Logger.Error
	_ func(EventBus, context.Context, Event) error                                     = EventBus.Publish
	_ func(ReadCloser, []byte) (int, error)                                            = ReadCloser.Read
	_ func(ReadCloser) error                                                           = ReadCloser.Close
	_ func(Transformer, context.Context, ImmutableBytes) (ImmutableBytes, error)       = Transformer.Transform
	_ func(Validator, context.Context, ImmutableBytes) ([]ValidationResult, error)     = Validator.Validate
	_ func(Observer, context.Context, Event) error                                     = Observer.OnEvent
	_ func(JobHandler, context.Context, ImmutableBytes) (ImmutableBytes, error)        = JobHandler.HandleJob
	_ func(EventHandler, context.Context, Event) error                                 = EventHandler.HandleEvent
	_ func(CommandHandler, context.Context, CommandInput) (CommandOutput, error)       = CommandHandler.Execute
)

func TestContractInterfaceMethodCounts(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want int
	}{
		{"Plugin", reflect.TypeOf((*Plugin)(nil)).Elem(), 2},
		{"Host", reflect.TypeOf((*Host)(nil)).Elem(), 14},
		{"Settings", reflect.TypeOf((*Settings)(nil)).Elem(), 4},
		{"Library", reflect.TypeOf((*Library)(nil)).Elem(), 2},
		{"BlobStore", reflect.TypeOf((*BlobStore)(nil)).Elem(), 3},
		{"Logger", reflect.TypeOf((*Logger)(nil)).Elem(), 4},
		{"EventBus", reflect.TypeOf((*EventBus)(nil)).Elem(), 1},
		{"ReadCloser", reflect.TypeOf((*ReadCloser)(nil)).Elem(), 2},
		{"Transformer", reflect.TypeOf((*Transformer)(nil)).Elem(), 1},
		{"Validator", reflect.TypeOf((*Validator)(nil)).Elem(), 1},
		{"Observer", reflect.TypeOf((*Observer)(nil)).Elem(), 1},
		{"JobHandler", reflect.TypeOf((*JobHandler)(nil)).Elem(), 1},
		{"EventHandler", reflect.TypeOf((*EventHandler)(nil)).Elem(), 1},
		{"CommandHandler", reflect.TypeOf((*CommandHandler)(nil)).Elem(), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.typ.Kind() != reflect.Interface {
				t.Fatalf("%s must be an interface, got %s", tt.name, tt.typ.Kind())
			}
			if got := tt.typ.NumMethod(); got != tt.want {
				t.Fatalf("%s has %d public methods, want exactly %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestContractSignatureNegativeControl(t *testing.T) {
	plugin := reflect.TypeOf((*Plugin)(nil)).Elem()
	register := reflect.TypeOf((func(context.Context, Host) error)(nil))
	if !interfaceMethodHasType(plugin, "Register", register) {
		t.Fatal("Plugin.Register must match the production signature expectation")
	}

	// Mutating the expected argument list must fail the same full-type oracle.
	mutatedExpectation := reflect.TypeOf((func(context.Context, Host, string) error)(nil))
	if interfaceMethodHasType(plugin, "Register", mutatedExpectation) {
		t.Fatal("signature oracle accepted a mutated Plugin.Register expectation")
	}
}

func interfaceMethodHasType(iface reflect.Type, name string, want reflect.Type) bool {
	method, ok := iface.MethodByName(name)
	return ok && method.Type == want
}

func TestContractRegistrationOwnership(t *testing.T) {
	registrations := []interface{}{
		ProviderRegistration{},
		TransformerRegistration{},
		ValidatorRegistration{},
		ObserverRegistration{},
		JobHandlerRegistration{},
		EventHandlerRegistration{},
		CommandRegistration{},
		UIRegistration{},
	}
	for _, registration := range registrations {
		if typ := reflect.TypeOf(registration); typ.Kind() != reflect.Struct {
			t.Errorf("%s must be a value DTO, got %s", typ.Name(), typ.Kind())
		}
	}
}

func TestContractMessagePayloadJSONKeys(t *testing.T) {
	tests := []struct {
		name  string
		typ   reflect.Type
		field string
	}{
		{"Event", reflect.TypeOf(Event{}), "Payload"},
		{"CommandInput", reflect.TypeOf(CommandInput{}), "Payload"},
		{"CommandOutput", reflect.TypeOf(CommandOutput{}), "Payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, ok := tt.typ.FieldByName(tt.field)
			if !ok {
				t.Fatalf("%s is missing %s", tt.name, tt.field)
			}
			if got := field.Tag.Get("json"); got != "payload" {
				t.Fatalf("%s.%s JSON key = %q, want payload", tt.name, tt.field, got)
			}
		})
	}
}

func TestContractImmutableBytesOwnership(t *testing.T) {
	var zero ImmutableBytes
	if zero.Len() != 0 || zero.Bytes() != nil {
		t.Fatal("zero ImmutableBytes must be empty and return nil bytes")
	}

	source := []byte{1, 2, 3}
	immutable := NewImmutableBytes(source)
	source[0] = 99
	if got := immutable.Bytes(); !reflect.DeepEqual(got, []byte{1, 2, 3}) {
		t.Fatalf("construction retained source ownership: %v", got)
	}

	copyForCaller := immutable.Bytes()
	copyForCaller[0] = 42
	if got := immutable.Bytes(); !reflect.DeepEqual(got, []byte{1, 2, 3}) {
		t.Fatalf("Bytes returned retained ownership: %v", got)
	}
	if immutable.Equal(NewImmutableBytes([]byte{1, 2, 4})) {
		t.Fatal("ImmutableBytes.Equal accepted a different payload")
	}
}

func TestContractEventPayloadOwnership(t *testing.T) {
	source := []byte("event payload")
	event := NewEvent("event-1", "library.import.created", "core", source)
	source[0] = 'E'
	if got := string(event.Payload.Bytes()); got != "event payload" {
		t.Fatalf("NewEvent retained caller payload ownership: %q", got)
	}

	callerCopy := event.Payload.Bytes()
	callerCopy[0] = 'X'
	if got := string(event.Payload.Bytes()); got != "event payload" {
		t.Fatalf("Event payload exposed mutable ownership: %q", got)
	}
}

func TestContractLanguage(t *testing.T) {
	if Language("").IsValid() || !Language("nl").IsValid() || !Language("en-US").IsValid() {
		t.Fatal("Language validity contract changed")
	}
}

func TestContractCapabilityIDs(t *testing.T) {
	expected := []CapabilityID{
		CapImport, CapProbe, CapExtraction, CapSegmentation,
		CapSTT, CapAlignment, CapTranslation, CapAnalysis,
		CapTTS, CapLessonRendering, CapExport,
	}
	if got := len(ValidCapabilities); got != len(expected) {
		t.Fatalf("ValidCapabilities has %d entries, want %d", got, len(expected))
	}
	for _, capability := range expected {
		if capability == "" || !capability.IsValid() {
			t.Errorf("capability %q must be declared and valid", capability)
		}
	}
	if CapabilityID("unknown").IsValid() {
		t.Fatal("unknown capability must remain invalid")
	}
}

func TestContractPriorityAndStaleReasons(t *testing.T) {
	if PriorityHigh >= PriorityDefault || PriorityDefault >= PriorityLow {
		t.Fatal("priority ordering contract changed")
	}
	for _, reason := range []StaleReason{
		StaleUpstreamEdit,
		StaleProviderChange,
		StaleParameterChange,
		StaleManualInvalidation,
	} {
		if reason == "" {
			t.Fatal("stale reason must not be empty")
		}
	}
}

func TestContractNoProjectInternalDependencies(t *testing.T) {
	cmd := exec.Command(
		"go", "list", "-deps", "-f",
		"{{if and .Module (eq .Module.Path \"doublangu\")}}{{.ImportPath}}{{end}}",
		".",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("module-filtered dependency check failed: %v\n%s", err, output)
	}
	for _, importPath := range strings.Fields(string(output)) {
		if strings.HasPrefix(importPath, "doublangu/internal/") {
			t.Fatalf("public plugin API imports project-internal package %q", importPath)
		}
	}
}

type testTransformer struct{}

func (testTransformer) Transform(_ context.Context, input ImmutableBytes) (ImmutableBytes, error) {
	return input, nil
}

type testValidator struct{}

func (testValidator) Validate(_ context.Context, _ ImmutableBytes) ([]ValidationResult, error) {
	return nil, nil
}

type testObserver struct{}

func (testObserver) OnEvent(_ context.Context, _ Event) error { return nil }

type testJobHandler struct{}

func (testJobHandler) HandleJob(_ context.Context, payload ImmutableBytes) (ImmutableBytes, error) {
	return payload, nil
}

type testEventHandler struct{}

func (testEventHandler) HandleEvent(_ context.Context, _ Event) error { return nil }

type testCommandHandler struct{}

func (testCommandHandler) Execute(_ context.Context, _ CommandInput) (CommandOutput, error) {
	return CommandOutput{}, nil
}

func TestRoundTripRegistrationDTOs(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "ProviderRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					ProviderRegistration{ID: "provider", Capability: CapTranslation, Name: "Translator", Priority: PriorityHigh, Provider: struct{}{}},
					ProviderRegistration{ID: "provider", Capability: CapTranslation, Name: "Translator", Priority: PriorityHigh},
					"provider", func(value ProviderRegistration) bool { return value.Provider == nil },
				)
			},
		},
		{
			name: "TransformerRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					TransformerRegistration{ID: "transformer", Capability: CapExtraction, Priority: PriorityDefault, Transformer: testTransformer{}},
					TransformerRegistration{ID: "transformer", Capability: CapExtraction, Priority: PriorityDefault},
					"transformer", func(value TransformerRegistration) bool { return value.Transformer == nil },
				)
			},
		},
		{
			name: "ValidatorRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					ValidatorRegistration{ID: "validator", Capability: CapAnalysis, Validator: testValidator{}},
					ValidatorRegistration{ID: "validator", Capability: CapAnalysis},
					"validator", func(value ValidatorRegistration) bool { return value.Validator == nil },
				)
			},
		},
		{
			name: "ObserverRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					ObserverRegistration{ID: "observer", EventTypes: []string{"library.created", "library.updated"}, Observer: testObserver{}},
					ObserverRegistration{ID: "observer", EventTypes: []string{"library.created", "library.updated"}},
					"observer", func(value ObserverRegistration) bool { return value.Observer == nil },
				)
			},
		},
		{
			name: "JobHandlerRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					JobHandlerRegistration{JobType: "media.probe", Handler: testJobHandler{}},
					JobHandlerRegistration{JobType: "media.probe"},
					"handler", func(value JobHandlerRegistration) bool { return value.Handler == nil },
				)
			},
		},
		{
			name: "EventHandlerRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					EventHandlerRegistration{EventTypes: []string{"import.finished", "import.failed"}, Handler: testEventHandler{}},
					EventHandlerRegistration{EventTypes: []string{"import.finished", "import.failed"}},
					"handler", func(value EventHandlerRegistration) bool { return value.Handler == nil },
				)
			},
		},
		{
			name: "CommandRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					CommandRegistration{ID: "library.import", Label: "Import", Description: "Import a source", Category: "library", Handler: testCommandHandler{}},
					CommandRegistration{ID: "library.import", Label: "Import", Description: "Import a source", Category: "library"},
					"handler", func(value CommandRegistration) bool { return value.Handler == nil },
				)
			},
		},
		{
			name: "UIRegistration",
			run: func(t *testing.T) {
				assertRegistrationRoundTrip(t,
					UIRegistration{ID: "reader", Label: "Reader", Type: UITypePanel, Container: "library", Priority: PriorityLow, Icon: "book", SourceURL: "/plugins/reader.js"},
					UIRegistration{ID: "reader", Label: "Reader", Type: UITypePanel, Container: "library", Priority: PriorityLow, Icon: "book", SourceURL: "/plugins/reader.js"},
					"", nil,
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func assertRegistrationRoundTrip[T any](t *testing.T, original, want T, omittedKey string, implementationIsNil func(T) bool) {
	t.Helper()
	restored, encoded := jsonRoundTrip(t, original)
	if !reflect.DeepEqual(want, restored) {
		t.Fatalf("JSON round trip lost serialized data:\nwant: %#v\n got: %#v", want, restored)
	}
	if omittedKey != "" {
		assertJSONKeyAbsent(t, encoded, omittedKey)
		if !implementationIsNil(restored) {
			t.Fatalf("non-serializable implementation field %q was restored", omittedKey)
		}
	}
}

func TestRoundTripRegistrationMetadataNegativeControl(t *testing.T) {
	want := ProviderRegistration{ID: "provider", Capability: CapTranslation, Name: "Translator", Priority: PriorityHigh}
	restored, _ := jsonRoundTrip(t, want)
	if !reflect.DeepEqual(want, restored) {
		t.Fatal("registration metadata baseline did not round trip")
	}

	mutatedMetadata := restored
	mutatedMetadata.Name = "Changed label"
	if reflect.DeepEqual(want, mutatedMetadata) {
		t.Fatal("registration comparison accepted a mutated serialized metadata field")
	}
}

func TestRoundTripPublicDTOs(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "LibraryFilter",
			run: func(t *testing.T) {
				original := LibraryFilter{SourceLanguage: "nl", TargetLanguage: "en", Query: "De Avonden"}
				restored, _ := jsonRoundTrip(t, original)
				assertDeepEqual(t, original, restored)
			},
		},
		{
			name: "LibraryTitle",
			run: func(t *testing.T) {
				original := LibraryTitle{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Title: "De Avonden", Author: "Gerard Reve", SourceLanguage: "nl", TargetLanguage: "en", MediaCount: 3, CreatedAt: 1712345678000, UpdatedAt: 1712345679000}
				restored, _ := jsonRoundTrip(t, original)
				assertDeepEqual(t, original, restored)
			},
		},
		{
			name: "Event",
			run: func(t *testing.T) {
				payload := []byte("event payload")
				original := NewEvent("event-1", "library.import.created", "core", payload)
				original.Timestamp = 1712345678000
				restored, _ := jsonRoundTrip(t, original)
				if !eventsEqual(original, restored) {
					t.Fatalf("Event round trip mismatch:\nwant: %#v\n got: %#v", original, restored)
				}
			},
		},
		{
			name: "CommandInput",
			run: func(t *testing.T) {
				original := CommandInput{Args: []string{"first", "second"}, Payload: NewImmutableBytes([]byte("input payload"))}
				restored, _ := jsonRoundTrip(t, original)
				if !commandInputsEqual(original, restored) {
					t.Fatalf("CommandInput round trip mismatch:\nwant: %#v\n got: %#v", original, restored)
				}
			},
		},
		{
			name: "CommandOutput",
			run: func(t *testing.T) {
				original := CommandOutput{Body: "completed", Payload: NewImmutableBytes([]byte("output payload"))}
				restored, _ := jsonRoundTrip(t, original)
				if !commandOutputsEqual(original, restored) {
					t.Fatalf("CommandOutput round trip mismatch:\nwant: %#v\n got: %#v", original, restored)
				}
			},
		},
		{
			name: "Provenance",
			run: func(t *testing.T) {
				original := provenanceFixture()
				restored, _ := jsonRoundTrip(t, original)
				assertDeepEqual(t, original, restored)
			},
		},
		{
			name: "StaleMarker",
			run: func(t *testing.T) {
				original := StaleMarker{Reason: StaleProviderChange, Timestamp: 1712345679000, PreviousProvenance: provenanceFixture()}
				restored, _ := jsonRoundTrip(t, original)
				assertDeepEqual(t, original, restored)
			},
		},
		{
			name: "ValidationResult",
			run: func(t *testing.T) {
				original := ValidationResult{ValidatorID: "validator", PluginID: "plugin", Severity: "error", Message: "invalid UTF-8", Span: &ByteSpan{Start: 4, End: 12}}
				restored, _ := jsonRoundTrip(t, original)
				assertDeepEqual(t, original, restored)
			},
		},
		{
			name: "ByteSpan",
			run: func(t *testing.T) {
				original := ByteSpan{Start: 4, End: 12}
				restored, _ := jsonRoundTrip(t, original)
				assertDeepEqual(t, original, restored)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestRoundTripPayloadNegativeControl(t *testing.T) {
	want := CommandOutput{Body: "completed", Payload: NewImmutableBytes([]byte{1, 2, 3})}
	restored, _ := jsonRoundTrip(t, want)
	if !commandOutputsEqual(want, restored) {
		t.Fatal("payload comparison baseline did not round trip")
	}

	mutatedPayload := restored
	mutatedPayload.Payload = NewImmutableBytes([]byte{1, 2, 4})
	if commandOutputsEqual(want, mutatedPayload) {
		t.Fatal("payload comparison accepted a mutated payload byte")
	}
}

func TestRoundTripImmutableBytesJSON(t *testing.T) {
	original := NewImmutableBytes([]byte("Hello"))
	restored, _ := jsonRoundTrip(t, original)
	if !original.Equal(restored) {
		t.Fatal("ImmutableBytes JSON round trip changed payload bytes")
	}
}

func jsonRoundTrip[T any](t *testing.T, original T) (T, []byte) {
	t.Helper()
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal %T: %v", original, err)
	}
	var restored T
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("unmarshal %T: %v", original, err)
	}
	return restored, encoded
}

func assertJSONKeyAbsent(t *testing.T, encoded []byte, key string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode encoded JSON: %v", err)
	}
	if _, ok := object[key]; ok {
		t.Fatalf("non-serializable implementation field %q appeared in JSON: %s", key, encoded)
	}
}

func assertDeepEqual[T any](t *testing.T, want, got T) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("JSON round trip mismatch:\nwant: %#v\n got: %#v", want, got)
	}
}

func eventsEqual(want, got Event) bool {
	return want.ID == got.ID &&
		want.Type == got.Type &&
		want.Source == got.Source &&
		want.Timestamp == got.Timestamp &&
		want.Payload.Equal(got.Payload)
}

func commandInputsEqual(want, got CommandInput) bool {
	return reflect.DeepEqual(want.Args, got.Args) && want.Payload.Equal(got.Payload)
}

func commandOutputsEqual(want, got CommandOutput) bool {
	return want.Body == got.Body && want.Payload.Equal(got.Payload)
}

func provenanceFixture() Provenance {
	return Provenance{
		PluginID:      "com.example.translator",
		PluginVersion: "1.2.3",
		Capability:    CapTranslation,
		ProviderID:    "provider",
		ModelID:       "model-v2",
		Parameters: map[string]string{
			"temperature": "0.3",
			"voice":       "calm",
		},
		InputHashes: []string{"aaaaaaaa", "bbbbbbbb"},
		Timestamp:   1712345678000,
	}
}
