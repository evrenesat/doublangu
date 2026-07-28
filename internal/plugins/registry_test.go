package manifest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	v1 "doublangu/pkg/pluginapi/v1"
)

type fakePlugin struct {
	calls    int
	register func(context.Context, v1.Host) error
}

func (p *fakePlugin) Manifest() v1.Manifest { return v1.Manifest{} }
func (p *fakePlugin) Register(ctx context.Context, host v1.Host) error {
	p.calls++
	return p.register(ctx, host)
}

type fakeTransformer struct{}

func (fakeTransformer) Transform(_ context.Context, input v1.ImmutableBytes) (v1.ImmutableBytes, error) {
	return v1.NewImmutableBytes(input.Bytes()), nil
}

type fakeValidator struct {
	findings []v1.ValidationResult
	err      error
	calls    *int
}

func (v fakeValidator) Validate(_ context.Context, _ v1.ImmutableBytes) ([]v1.ValidationResult, error) {
	if v.calls != nil {
		*v.calls++
	}
	return v.findings, v.err
}

type fakeObserver struct {
	mu      sync.Mutex
	events  []v1.Event
	err     error
	fail    int
	started chan struct{}
	release chan struct{}
}

func (o *fakeObserver) OnEvent(_ context.Context, event v1.Event) error {
	if o.started != nil {
		select {
		case o.started <- struct{}{}:
		default:
		}
	}
	if o.release != nil {
		<-o.release
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
	if o.fail > 0 {
		o.fail--
		return errors.New("observer failed")
	}
	return o.err
}

func (o *fakeObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.events)
}

type fakeJobHandler struct{}

func (fakeJobHandler) HandleJob(context.Context, v1.ImmutableBytes) (v1.ImmutableBytes, error) {
	return v1.ImmutableBytes{}, nil
}

type fakeEventHandler struct{}

func (fakeEventHandler) HandleEvent(context.Context, v1.Event) error { return nil }

type fakeCommandHandler struct{}

func (fakeCommandHandler) Execute(context.Context, v1.CommandInput) (v1.CommandOutput, error) {
	return v1.CommandOutput{}, nil
}

type registrationSet struct {
	provider            string
	transformer         string
	validator           string
	validatorCapability v1.CapabilityID
	observer            string
	job                 string
	eventTypes          []string
	command             string
	ui                  string
}

func namedSet(prefix string) registrationSet {
	return registrationSet{
		provider: prefix + ".provider", transformer: prefix + ".transformer",
		validator: prefix + ".validator", validatorCapability: v1.CapAnalysis,
		observer: prefix + ".observer",
		job:      prefix + ".job", eventTypes: []string{prefix + ".event"},
		command: prefix + ".command", ui: prefix + ".ui",
	}
}

// registerAll attempts every surface even if a previous Host registration
// rejected. That makes sticky-error and all-surface rollback tests exercise the
// same production Plugin.Register boundary used by normal plugins.
func registerAll(host v1.Host, registrations registrationSet, observer v1.Observer) error {
	var first error
	record := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	record(host.RegisterProvider(v1.ProviderRegistration{ID: registrations.provider, Capability: v1.CapAnalysis, Provider: struct{}{}}))
	record(host.RegisterTransformer(v1.TransformerRegistration{ID: registrations.transformer, Capability: v1.CapAnalysis, Transformer: fakeTransformer{}}))
	record(host.RegisterValidator(v1.ValidatorRegistration{ID: registrations.validator, Capability: registrations.validatorCapability, Validator: fakeValidator{}}))
	record(host.RegisterObserver(v1.ObserverRegistration{ID: registrations.observer, EventTypes: []string{"delivery.event"}, Observer: observer}))
	record(host.RegisterJobHandler(v1.JobHandlerRegistration{JobType: registrations.job, Handler: fakeJobHandler{}}))
	record(host.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: registrations.eventTypes, Handler: fakeEventHandler{}}))
	record(host.RegisterCommand(v1.CommandRegistration{ID: registrations.command, Handler: fakeCommandHandler{}}))
	record(host.RegisterUI(v1.UIRegistration{ID: registrations.ui, Type: v1.UITypePanel}))
	return first
}

func pluginRegisteringAll(registrations registrationSet, observer v1.Observer) *fakePlugin {
	return &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		return registerAll(host, registrations, observer)
	}}
}

type registryCounts struct {
	providers, transformers, validators, observers int
	jobs, events, commands, uis                    int
}

func committedCounts(registry *Registry) registryCounts {
	return registryCounts{
		providers: registry.ProviderCount(), transformers: registry.TransformerCount(),
		validators: registry.ValidatorCount(), observers: registry.ObserverCount(),
		jobs: registry.JobHandlerCount(), events: registry.EventHandlerCount(),
		commands: registry.CommandCount(), uis: registry.UICount(),
	}
}

func allCounts(value int) registryCounts {
	return registryCounts{value, value, value, value, value, value, value, value}
}

func requireCounts(t *testing.T, registry *Registry, want registryCounts) {
	t.Helper()
	if got := committedCounts(registry); got != want {
		t.Fatalf("committed counts = %+v, want %+v", got, want)
	}
}

func requireSetRetained(t *testing.T, registry *Registry, registrations registrationSet) {
	t.Helper()
	if _, err := registry.ResolveProvider(registrations.provider); err != nil {
		t.Fatalf("seed provider missing: %v", err)
	}
	if _, err := registry.ResolveJobHandler(registrations.job); err != nil {
		t.Fatalf("seed job handler missing: %v", err)
	}
	if _, ok := registry.commands[registrations.command]; !ok {
		t.Fatal("seed command missing")
	}
	if _, ok := registry.uis[registrations.ui]; !ok {
		t.Fatal("seed UI missing")
	}
	transformers := registry.ListTransformers()
	if len(transformers) != 1 || transformers[0].id != registrations.transformer {
		t.Fatalf("seed transformer changed: %+v", transformers)
	}
	validators := registry.ListValidators(registrations.validatorCapability)
	if len(validators) != 1 || validators[0].id != registrations.validator {
		t.Fatalf("seed validator changed: %+v", validators)
	}
	if len(registry.observers) != 1 || registry.observers[0].id != registrations.observer {
		t.Fatalf("seed observer changed: %+v", registry.observers)
	}
	_, eventKey, err := canonicalEventTypes(registrations.eventTypes)
	if err != nil || len(registry.eventHandlers) != 1 || registry.eventHandlers[0].eventKey != eventKey {
		t.Fatalf("seed event subscription changed: %+v, %v", registry.eventHandlers, err)
	}
}

// Minimal concrete services prove the transaction host returns the exact six
// injected dependencies, not synthetic registration-only placeholders.
type fakeSettings struct{}

func (fakeSettings) Get(string) (v1.ImmutableBytes, error) { return v1.ImmutableBytes{}, nil }
func (fakeSettings) Set(string, []byte) error              { return nil }
func (fakeSettings) Delete(string) error                   { return nil }
func (fakeSettings) List() ([]string, error)               { return nil, nil }

type fakeLibrary struct{}

func (fakeLibrary) ListTitles(context.Context, *v1.LibraryFilter, int, int) ([]v1.LibraryTitle, error) {
	return nil, nil
}
func (fakeLibrary) GetTitle(context.Context, string) (*v1.LibraryTitle, error) { return nil, nil }

type fakeBlobs struct{}

func (fakeBlobs) Put(context.Context, []byte) (string, error) { return "", nil }
func (fakeBlobs) Get(context.Context, string) (v1.ImmutableBytes, error) {
	return v1.ImmutableBytes{}, nil
}
func (fakeBlobs) Exists(context.Context, string) (bool, error) { return false, nil }

type fakeLogger struct{}

func (fakeLogger) Debug(string, ...interface{}) {}
func (fakeLogger) Info(string, ...interface{})  {}
func (fakeLogger) Warn(string, ...interface{})  {}
func (fakeLogger) Error(string, ...interface{}) {}

type fakeEventBus struct{}

func (fakeEventBus) Publish(context.Context, v1.Event) error { return nil }

func TestRegistryProductionRegistrationBoundary(t *testing.T) {
	registry := NewRegistry()
	settings := fakeSettings{}
	library := fakeLibrary{}
	blobs := fakeBlobs{}
	logger := fakeLogger{}
	client := &http.Client{}
	bus := fakeEventBus{}
	plugin := &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		if host.Settings() != settings || host.Library() != library || host.Blobs() != blobs ||
			host.Logger() != logger || host.HTTPClient() != client || host.EventBus() != bus {
			return errors.New("transaction host did not return injected services")
		}
		return registerAll(host, namedSet("accepted"), &fakeObserver{})
	}}

	if err := registry.RegisterPlugin(context.Background(), "accepted-plugin", plugin, settings, library, blobs, logger, client, bus); err != nil {
		t.Fatal(err)
	}
	if plugin.calls != 1 {
		t.Fatalf("Plugin.Register calls = %d, want 1", plugin.calls)
	}
	requireCounts(t, registry, allCounts(1))
}

func TestRegistryReturnedErrorAndPanicRollbackEverySurface(t *testing.T) {
	for _, test := range []struct {
		name   string
		plugin *fakePlugin
	}{
		{
			name: "returned error",
			plugin: &fakePlugin{register: func(_ context.Context, host v1.Host) error {
				if err := registerAll(host, namedSet("candidate-return"), &fakeObserver{}); err != nil {
					return err
				}
				return errors.New("candidate rejected itself")
			}},
		},
		{
			name: "genuine panic",
			plugin: &fakePlugin{register: func(_ context.Context, host v1.Host) error {
				if err := registerAll(host, namedSet("candidate-panic"), &fakeObserver{}); err != nil {
					return err
				}
				panic("real registration panic")
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.RegisterPlugin(context.Background(), "seed", pluginRegisteringAll(namedSet("seed"), &fakeObserver{}), nil, nil, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			err := registry.RegisterPlugin(context.Background(), "candidate", test.plugin, nil, nil, nil, nil, nil, nil)
			if err == nil {
				t.Fatal("expected registration failure")
			}
			if test.name == "genuine panic" && !strings.Contains(err.Error(), "panicked") {
				t.Fatalf("panic recovery error = %v", err)
			}
			if test.plugin.calls != 1 {
				t.Fatalf("Plugin.Register calls = %d, want 1", test.plugin.calls)
			}
			requireCounts(t, registry, allCounts(1))
		})
	}
}

func TestRegistryStickyAndTerminalTransactions(t *testing.T) {
	registry := NewRegistry()
	ignoredError := &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		_ = host.RegisterProvider(v1.ProviderRegistration{})
		_ = host.RegisterProvider(v1.ProviderRegistration{ID: "would-leak", Capability: v1.CapAnalysis})
		return nil
	}}
	if err := registry.RegisterPlugin(context.Background(), "candidate", ignoredError, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("ignored Host error must prevent commit")
	}
	requireCounts(t, registry, allCounts(0))

	rollbackRegistrations := namedSet("rollback")
	rolledBack := registry.Begin("rollback")
	for _, registration := range []struct {
		surface string
		add     func() error
	}{
		{"provider", func() error {
			return rolledBack.AddProvider(v1.ProviderRegistration{ID: rollbackRegistrations.provider, Capability: v1.CapAnalysis, Provider: struct{}{}})
		}},
		{"transformer", func() error {
			return rolledBack.AddTransformer(v1.TransformerRegistration{ID: rollbackRegistrations.transformer, Capability: v1.CapAnalysis, Transformer: fakeTransformer{}})
		}},
		{"validator", func() error {
			return rolledBack.AddValidator(v1.ValidatorRegistration{ID: rollbackRegistrations.validator, Capability: rollbackRegistrations.validatorCapability, Validator: fakeValidator{}})
		}},
		{"observer", func() error {
			return rolledBack.AddObserver(v1.ObserverRegistration{ID: rollbackRegistrations.observer, EventTypes: []string{"rollback.event"}, Observer: &fakeObserver{}})
		}},
		{"job handler", func() error {
			return rolledBack.AddJobHandler(v1.JobHandlerRegistration{JobType: rollbackRegistrations.job, Handler: fakeJobHandler{}})
		}},
		{"event handler", func() error {
			return rolledBack.AddEventHandler(v1.EventHandlerRegistration{EventTypes: rollbackRegistrations.eventTypes, Handler: fakeEventHandler{}})
		}},
		{"command", func() error {
			return rolledBack.AddCommand(v1.CommandRegistration{ID: rollbackRegistrations.command, Handler: fakeCommandHandler{}})
		}},
		{"UI", func() error {
			return rolledBack.AddUI(v1.UIRegistration{ID: rollbackRegistrations.ui, Type: v1.UITypePanel})
		}},
	} {
		if err := registration.add(); err != nil {
			t.Fatalf("add rollback %s: %v", registration.surface, err)
		}
	}
	rolledBack.Rollback()
	if err := rolledBack.Commit(); err == nil {
		t.Fatal("rollback followed by commit must fail")
	}
	if err := rolledBack.AddCommand(v1.CommandRegistration{ID: "rollback.command"}); err == nil {
		t.Fatal("registration after rollback must fail")
	}
	requireCounts(t, registry, allCounts(0))

	committed := registry.Begin("committed")
	if err := committed.AddProvider(v1.ProviderRegistration{ID: "committed.provider"}); err != nil {
		t.Fatal(err)
	}
	if err := committed.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := committed.Commit(); err == nil {
		t.Fatal("repeated commit must fail")
	}
	if err := committed.AddCommand(v1.CommandRegistration{ID: "after.commit"}); err == nil {
		t.Fatal("registration after commit must fail")
	}
	committed.Rollback()
	if _, err := registry.ResolveProvider("committed.provider"); err != nil {
		t.Fatalf("rollback after commit must not unpublish: %v", err)
	}
}

func TestRegistryLateConflictMatrixRollsBackCandidate(t *testing.T) {
	for _, test := range []struct {
		name       string
		seedPlugin string
		seed       registrationSet
		candidate  registrationSet
	}{
		{"provider", "seed", namedSet("seed-provider"), func() registrationSet {
			s := namedSet("candidate-provider")
			s.provider = "seed-provider.provider"
			return s
		}()},
		{"transformer", "candidate", namedSet("seed-transformer"), func() registrationSet {
			s := namedSet("candidate-transformer")
			s.transformer = "seed-transformer.transformer"
			return s
		}()},
		{"validator", "candidate", namedSet("seed-validator"), func() registrationSet {
			s := namedSet("candidate-validator")
			s.validator = "seed-validator.validator"
			return s
		}()},
		{"observer", "candidate", namedSet("seed-observer"), func() registrationSet {
			s := namedSet("candidate-observer")
			s.observer = "seed-observer.observer"
			return s
		}()},
		{"job type", "seed", namedSet("seed-job"), func() registrationSet { s := namedSet("candidate-job"); s.job = "seed-job.job"; return s }()},
		{"event subscription", "candidate", func() registrationSet {
			s := namedSet("seed-event")
			s.eventTypes = []string{"alpha", "beta"}
			return s
		}(), func() registrationSet {
			s := namedSet("candidate-event")
			s.eventTypes = []string{"beta", "alpha"}
			return s
		}()},
		{"command", "seed", namedSet("seed-command"), func() registrationSet {
			s := namedSet("candidate-command")
			s.command = "seed-command.command"
			return s
		}()},
		{"UI", "seed", namedSet("seed-ui"), func() registrationSet { s := namedSet("candidate-ui"); s.ui = "seed-ui.ui"; return s }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.RegisterPlugin(context.Background(), test.seedPlugin, pluginRegisteringAll(test.seed, &fakeObserver{}), nil, nil, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := registry.RegisterPlugin(context.Background(), "candidate", pluginRegisteringAll(test.candidate, &fakeObserver{}), nil, nil, nil, nil, nil, nil); err == nil {
				t.Fatal("expected exact committed-state conflict")
			}
			requireCounts(t, registry, allCounts(1))
			requireSetRetained(t, registry, test.seed)
		})
	}
}

func TestRegistryCrossCapabilityValidatorConflictRollsBackCandidate(t *testing.T) {
	registry := NewRegistry()
	seed := namedSet("cross-capability-seed")
	if err := registry.RegisterPlugin(context.Background(), "candidate", pluginRegisteringAll(seed, &fakeObserver{}), nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	candidate := namedSet("cross-capability-candidate")
	candidate.validator = seed.validator
	candidate.validatorCapability = v1.CapTranslation
	if err := registry.RegisterPlugin(context.Background(), "candidate", pluginRegisteringAll(candidate, &fakeObserver{}), nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("same plugin validator ID must conflict across capabilities")
	}
	requireCounts(t, registry, allCounts(1))
	requireSetRetained(t, registry, seed)
	if got := registry.ListValidators(v1.CapTranslation); len(got) != 0 {
		t.Fatalf("candidate validator leaked into different capability: %+v", got)
	}
}

func TestRegistryIntraTransactionDuplicateMatrixAndNegativeControls(t *testing.T) {
	duplicate := []struct {
		name string
		call func(v1.Host) error
	}{
		{"provider", func(h v1.Host) error {
			_ = h.RegisterProvider(v1.ProviderRegistration{ID: "shared"})
			return h.RegisterProvider(v1.ProviderRegistration{ID: "shared"})
		}},
		{"transformer", func(h v1.Host) error {
			_ = h.RegisterTransformer(v1.TransformerRegistration{ID: "shared"})
			return h.RegisterTransformer(v1.TransformerRegistration{ID: "shared"})
		}},
		{"validator", func(h v1.Host) error {
			_ = h.RegisterValidator(v1.ValidatorRegistration{ID: "shared"})
			return h.RegisterValidator(v1.ValidatorRegistration{ID: "shared"})
		}},
		{"observer", func(h v1.Host) error {
			_ = h.RegisterObserver(v1.ObserverRegistration{ID: "shared"})
			return h.RegisterObserver(v1.ObserverRegistration{ID: "shared"})
		}},
		{"job type", func(h v1.Host) error {
			_ = h.RegisterJobHandler(v1.JobHandlerRegistration{JobType: "shared"})
			return h.RegisterJobHandler(v1.JobHandlerRegistration{JobType: "shared"})
		}},
		{"event canonicalization", func(h v1.Host) error {
			_ = h.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: []string{"a", "b"}})
			return h.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: []string{"b", "a"}})
		}},
		{"all events", func(h v1.Host) error {
			_ = h.RegisterEventHandler(v1.EventHandlerRegistration{})
			return h.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: []string{}})
		}},
		{"command", func(h v1.Host) error {
			_ = h.RegisterCommand(v1.CommandRegistration{ID: "shared"})
			return h.RegisterCommand(v1.CommandRegistration{ID: "shared"})
		}},
		{"UI", func(h v1.Host) error {
			_ = h.RegisterUI(v1.UIRegistration{ID: "shared"})
			return h.RegisterUI(v1.UIRegistration{ID: "shared"})
		}},
		{"repeated event type", func(h v1.Host) error {
			return h.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: []string{"a", "a"}})
		}},
	}
	for _, test := range duplicate {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			plugin := &fakePlugin{register: func(_ context.Context, host v1.Host) error { return test.call(host) }}
			if err := registry.RegisterPlugin(context.Background(), "same-plugin", plugin, nil, nil, nil, nil, nil, nil); err == nil {
				t.Fatal("expected duplicate registration failure")
			}
			requireCounts(t, registry, allCounts(0))
		})
	}

	registry := NewRegistry()
	for _, pluginID := range []string{"plugin-a", "plugin-b"} {
		plugin := &fakePlugin{register: func(_ context.Context, host v1.Host) error {
			if err := host.RegisterTransformer(v1.TransformerRegistration{ID: "same", Transformer: fakeTransformer{}}); err != nil {
				return err
			}
			if err := host.RegisterValidator(v1.ValidatorRegistration{ID: "same", Validator: fakeValidator{}}); err != nil {
				return err
			}
			return host.RegisterObserver(v1.ObserverRegistration{ID: "same", Observer: &fakeObserver{}})
		}}
		if err := registry.RegisterPlugin(context.Background(), pluginID, plugin, nil, nil, nil, nil, nil, nil); err != nil {
			t.Fatalf("same handler IDs must be independent across plugins: %v", err)
		}
	}
	if registry.TransformerCount() != 2 || registry.ValidatorCount() != 2 || registry.ObserverCount() != 2 {
		t.Fatalf("cross-plugin handler IDs were collapsed: %+v", committedCounts(registry))
	}

	eventTypes := []string{"first"}
	eventPlugin := &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		if err := host.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: eventTypes}); err != nil {
			return err
		}
		return host.RegisterEventHandler(v1.EventHandlerRegistration{EventTypes: []string{"second"}})
	}}
	if err := registry.RegisterPlugin(context.Background(), "event-plugin", eventPlugin, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	eventTypes[0] = "mutated"
	handlers := registry.ListEventHandlers()
	if len(handlers) != 2 || handlers[0].eventTypes[0] != "first" {
		t.Fatalf("event subscriptions were not copied/canonicalized: %+v", handlers)
	}
	handlers[0].eventTypes[0] = "caller-mutation"
	if registry.ListEventHandlers()[0].eventTypes[0] == "caller-mutation" {
		t.Fatal("ListEventHandlers exposed registry-owned event type slice")
	}
}

func TestRegistryAllEventsSubscriptionConflictsWithCommittedState(t *testing.T) {
	registry := NewRegistry()
	seed := namedSet("all-events-seed")
	seed.eventTypes = nil
	if err := registry.RegisterPlugin(context.Background(), "event-plugin", pluginRegisteringAll(seed, &fakeObserver{}), nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	candidate := namedSet("all-events-candidate")
	candidate.eventTypes = []string{}
	if err := registry.RegisterPlugin(context.Background(), "event-plugin", pluginRegisteringAll(candidate, &fakeObserver{}), nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("all-events subscription must conflict with its committed canonical key")
	}
	requireCounts(t, registry, allCounts(1))
	requireSetRetained(t, registry, seed)
}

func TestRegistryTransformerValidatorAndProviderDeterminism(t *testing.T) {
	registry := NewRegistry()
	transformer := func(pluginID, id string, priority v1.Priority) error {
		return registry.RegisterPlugin(context.Background(), pluginID, &fakePlugin{register: func(_ context.Context, host v1.Host) error {
			return host.RegisterTransformer(v1.TransformerRegistration{ID: id, Priority: priority, Transformer: fakeTransformer{}})
		}}, nil, nil, nil, nil, nil, nil)
	}
	for _, entry := range []struct {
		plugin, id string
		priority   v1.Priority
	}{{"z", "b", 100}, {"a", "b", 10}, {"a", "a", 10}} {
		if err := transformer(entry.plugin, entry.id, entry.priority); err != nil {
			t.Fatal(err)
		}
	}
	gotTransformers := registry.ListTransformers()
	gotOrder := []string{gotTransformers[0].pluginID + "/" + gotTransformers[0].id, gotTransformers[1].pluginID + "/" + gotTransformers[1].id, gotTransformers[2].pluginID + "/" + gotTransformers[2].id}
	if strings.Join(gotOrder, ",") != "a/a,a/b,z/b" {
		t.Fatalf("transformer order = %v", gotOrder)
	}

	validatorCalls := 0
	registerValidator := func(pluginID, id string, findings []v1.ValidationResult, err error) {
		t.Helper()
		if registrationErr := registry.RegisterPlugin(context.Background(), pluginID, &fakePlugin{register: func(_ context.Context, host v1.Host) error {
			return host.RegisterValidator(v1.ValidatorRegistration{ID: id, Capability: v1.CapAnalysis, Validator: fakeValidator{findings: findings, err: err, calls: &validatorCalls}})
		}}, nil, nil, nil, nil, nil, nil); registrationErr != nil {
			t.Fatal(registrationErr)
		}
	}
	registerValidator("plugin-b", "b", []v1.ValidationResult{{Message: "b"}}, errors.New("b-error"))
	registerValidator("plugin-a", "two", []v1.ValidationResult{{Message: "a2"}}, errors.New("a2-error"))
	registerValidator("plugin-a", "one", []v1.ValidationResult{{Message: "a1"}}, nil)
	findings, err := registry.Validate(context.Background(), v1.CapAnalysis, v1.NewImmutableBytes([]byte("input")))
	if err == nil || validatorCalls != 3 {
		t.Fatalf("validators did not all execute: calls=%d err=%v", validatorCalls, err)
	}
	if got := strings.Join([]string{findings[0].PluginID + "/" + findings[0].ValidatorID, findings[1].PluginID + "/" + findings[1].ValidatorID, findings[2].PluginID + "/" + findings[2].ValidatorID}, ","); got != "plugin-a/one,plugin-a/two,plugin-b/b" {
		t.Fatalf("findings order = %s", got)
	}
	if !strings.Contains(err.Error(), "a2-error") || !strings.Contains(err.Error(), "b-error") || strings.Index(err.Error(), "a2-error") > strings.Index(err.Error(), "b-error") {
		t.Fatalf("validator aggregate is incomplete or nondeterministic: %v", err)
	}

	providerPlugin := &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		if err := host.RegisterProvider(v1.ProviderRegistration{ID: "first", Priority: v1.PriorityHigh}); err != nil {
			return err
		}
		return host.RegisterProvider(v1.ProviderRegistration{ID: "second", Priority: v1.PriorityLow})
	}}
	if err := registry.RegisterPlugin(context.Background(), "providers", providerPlugin, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveProvider("missing"); err == nil {
		t.Fatal("unknown selected provider must not resolve a fallback")
	}
	if provider, err := registry.ResolveProvider("second"); err != nil || provider.id != "second" {
		t.Fatalf("explicit provider resolution = %+v, %v", provider, err)
	}
}

func TestRegistryObserverTupleRetryAndImmutability(t *testing.T) {
	registry := NewRegistry()
	observerA := &fakeObserver{}
	observerB := &fakeObserver{}
	observerOtherID := &fakeObserver{}
	for _, registration := range []struct {
		plugin, id string
		observer   *fakeObserver
	}{{"plugin-a", "same", observerA}, {"plugin-b", "same", observerB}, {"plugin-a", "other", observerOtherID}} {
		registration := registration
		if err := registry.RegisterPlugin(context.Background(), registration.plugin, &fakePlugin{register: func(_ context.Context, host v1.Host) error {
			return host.RegisterObserver(v1.ObserverRegistration{ID: registration.id, EventTypes: []string{"wanted"}, Observer: registration.observer})
		}}, nil, nil, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	first := v1.NewEvent("one", "wanted", "core", []byte("stable"))
	if err := registry.DispatchEvent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := registry.DispatchEvent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := v1.NewEvent("two", "wanted", "core", []byte("stable"))
	if err := registry.DispatchEvent(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	for _, observer := range []*fakeObserver{observerA, observerB, observerOtherID} {
		if observer.count() != 2 {
			t.Fatalf("tuple dimensions were not independent: got %d", observer.count())
		}
	}
	if registry.ObservedEventCount() != 6 {
		t.Fatalf("completed tuple count = %d, want 6", registry.ObservedEventCount())
	}

	retryRegistry := NewRegistry()
	retryObserver := &fakeObserver{fail: 1}
	if err := retryRegistry.RegisterPlugin(context.Background(), "retry", &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		return host.RegisterObserver(v1.ObserverRegistration{ID: "observer", Observer: retryObserver})
	}}, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	retryEvent := v1.NewEvent("retry", "wanted", "core", nil)
	if err := retryRegistry.DispatchEvent(context.Background(), retryEvent); err == nil {
		t.Fatal("first delivery must fail")
	}
	if err := retryRegistry.DispatchEvent(context.Background(), retryEvent); err != nil {
		t.Fatal(err)
	}
	if retryObserver.count() != 2 || retryRegistry.ObservedEventCount() != 1 {
		t.Fatal("failed delivery did not release its tuple for retry")
	}

	concurrentRegistry := NewRegistry()
	blocking := &fakeObserver{started: make(chan struct{}, 1), release: make(chan struct{})}
	if err := concurrentRegistry.RegisterPlugin(context.Background(), "concurrent", &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		return host.RegisterObserver(v1.ObserverRegistration{ID: "observer", Observer: blocking})
	}}, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	concurrentEvent := v1.NewEvent("same", "wanted", "core", nil)
	done := make(chan error, 2)
	go func() { done <- concurrentRegistry.DispatchEvent(context.Background(), concurrentEvent) }()
	<-blocking.started
	go func() { done <- concurrentRegistry.DispatchEvent(context.Background(), concurrentEvent) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if blocking.count() != 1 {
		t.Fatalf("concurrent tuple delivered %d times", blocking.count())
	}

	immutableRegistry := NewRegistry()
	filters := []string{"wanted"}
	immutableObserver := &fakeObserver{}
	if err := immutableRegistry.RegisterPlugin(context.Background(), "immutable", &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		return host.RegisterObserver(v1.ObserverRegistration{ID: "observer", EventTypes: filters, Observer: immutableObserver})
	}}, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	filters[0] = "mutated"
	payload := []byte("original")
	event := v1.NewEvent("immutable", "wanted", "core", payload)
	payload[0] = 'X'
	if err := immutableRegistry.DispatchEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if immutableObserver.count() != 1 || string(immutableObserver.events[0].Payload.Bytes()) != "original" {
		t.Fatal("observer registration or event payload exposed caller mutation")
	}
}

func TestRegistryObserverPostCommitOnly(t *testing.T) {
	registry := NewRegistry()
	observer := &fakeObserver{}
	transaction := registry.Begin("pending")
	if err := transaction.AddObserver(v1.ObserverRegistration{ID: "observer", Observer: observer}); err != nil {
		t.Fatal(err)
	}
	if err := registry.DispatchEvent(context.Background(), v1.NewEvent("before", "event", "core", nil)); err != nil {
		t.Fatal(err)
	}
	if observer.count() != 0 {
		t.Fatal("observer ran before transaction commit")
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := registry.DispatchEvent(context.Background(), v1.NewEvent("after", "event", "core", nil)); err != nil {
		t.Fatal(err)
	}
	if observer.count() != 1 {
		t.Fatal("committed observer did not receive event")
	}
}

func TestRegistryInvalidPluginIDIsSticky(t *testing.T) {
	registry := NewRegistry()
	plugin := pluginRegisteringAll(namedSet("invalid"), &fakeObserver{})
	err := registry.RegisterPlugin(context.Background(), "", plugin, nil, nil, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "plugin ID") {
		t.Fatalf("empty plugin ID error = %v", err)
	}
	requireCounts(t, registry, allCounts(0))
}

func ExampleRegistry_RegisterPlugin() {
	registry := NewRegistry()
	plugin := &fakePlugin{register: func(_ context.Context, host v1.Host) error {
		return host.RegisterProvider(v1.ProviderRegistration{ID: "example-provider"})
	}}
	err := registry.RegisterPlugin(context.Background(), "example", plugin, nil, nil, nil, nil, nil, nil)
	fmt.Println(err == nil, registry.ProviderCount())
	// Output: true 1
}
