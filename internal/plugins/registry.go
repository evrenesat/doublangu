package manifest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	v1 "doublangu/pkg/pluginapi/v1"
)

// Registry holds committed plugin registrations. The mutex protects committed
// surfaces and observer delivery state; a PendingTransaction separately protects
// its uncommitted registrations.
type Registry struct {
	mu sync.Mutex

	providers     map[string]providerEntry
	transformers  []transformerEntry
	validators    map[v1.CapabilityID][]validatorEntry
	observers     []observerEntry
	jobHandlers   map[string]jobHandlerEntry
	eventHandlers []eventHandlerEntry
	commands      map[string]commandEntry
	uis           map[string]uiEntry

	// completedEvents records only successful observer deliveries. Deliveries in
	// progress reserve their tuple so concurrent dispatches do not call twice.
	completedEvents  map[observerTuple]bool
	deliveringEvents map[observerTuple]bool
}

type observerTuple struct {
	pluginID   string
	observerID string
	eventID    string
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{
		providers:        make(map[string]providerEntry),
		validators:       make(map[v1.CapabilityID][]validatorEntry),
		jobHandlers:      make(map[string]jobHandlerEntry),
		commands:         make(map[string]commandEntry),
		uis:              make(map[string]uiEntry),
		completedEvents:  make(map[observerTuple]bool),
		deliveringEvents: make(map[observerTuple]bool),
	}
}

// Begin starts one pending transaction for pluginID. Production registration
// should use RegisterPlugin so Plugin.Register, rollback, and commit share one
// explicit boundary; Begin remains useful for state-machine unit tests.
func (r *Registry) Begin(pluginID string) *PendingTransaction {
	return &PendingTransaction{pluginID: pluginID, registry: r, state: transactionOpen}
}

// RegisterPlugin is the sole production transaction coordinator. It gives one
// transaction-scoped Host to exactly one Plugin.Register invocation, recovers a
// panic, and commits only after Register returns nil and the transaction remains
// open without a sticky registration error.
func (r *Registry) RegisterPlugin(
	ctx context.Context,
	pluginID string,
	plugin v1.Plugin,
	settings v1.Settings,
	library v1.Library,
	blobs v1.BlobStore,
	logger v1.Logger,
	httpClient *http.Client,
	eventBus v1.EventBus,
) (err error) {
	if plugin == nil {
		return fmt.Errorf("plugin %q registration: plugin must not be nil", pluginID)
	}

	transaction := r.Begin(pluginID)
	host := &registrationHost{
		transaction: transaction,
		settings:    settings,
		library:     library,
		blobs:       blobs,
		logger:      logger,
		httpClient:  httpClient,
		eventBus:    eventBus,
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			transaction.Rollback()
			err = fmt.Errorf("plugin %q registration panicked: %v", pluginID, recovered)
			return
		}
		if err != nil {
			transaction.Rollback()
		}
	}()

	if registerErr := plugin.Register(ctx, host); registerErr != nil {
		return fmt.Errorf("plugin %q registration failed: %w", pluginID, registerErr)
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		return fmt.Errorf("plugin %q registration did not commit: %w", pluginID, commitErr)
	}
	return nil
}

// registrationHost supplies the six host services and routes every registration
// surface to the same pending transaction. It intentionally owns no registry
// mutation itself.
type registrationHost struct {
	transaction *PendingTransaction
	settings    v1.Settings
	library     v1.Library
	blobs       v1.BlobStore
	logger      v1.Logger
	httpClient  *http.Client
	eventBus    v1.EventBus
}

var _ v1.Host = (*registrationHost)(nil)

func (h *registrationHost) Settings() v1.Settings    { return h.settings }
func (h *registrationHost) Library() v1.Library      { return h.library }
func (h *registrationHost) Blobs() v1.BlobStore      { return h.blobs }
func (h *registrationHost) Logger() v1.Logger        { return h.logger }
func (h *registrationHost) HTTPClient() *http.Client { return h.httpClient }
func (h *registrationHost) EventBus() v1.EventBus    { return h.eventBus }
func (h *registrationHost) RegisterProvider(reg v1.ProviderRegistration) error {
	return h.transaction.AddProvider(reg)
}
func (h *registrationHost) RegisterTransformer(reg v1.TransformerRegistration) error {
	return h.transaction.AddTransformer(reg)
}
func (h *registrationHost) RegisterValidator(reg v1.ValidatorRegistration) error {
	return h.transaction.AddValidator(reg)
}
func (h *registrationHost) RegisterObserver(reg v1.ObserverRegistration) error {
	return h.transaction.AddObserver(reg)
}
func (h *registrationHost) RegisterJobHandler(reg v1.JobHandlerRegistration) error {
	return h.transaction.AddJobHandler(reg)
}
func (h *registrationHost) RegisterEventHandler(reg v1.EventHandlerRegistration) error {
	return h.transaction.AddEventHandler(reg)
}
func (h *registrationHost) RegisterCommand(reg v1.CommandRegistration) error {
	return h.transaction.AddCommand(reg)
}
func (h *registrationHost) RegisterUI(reg v1.UIRegistration) error {
	return h.transaction.AddUI(reg)
}

// commitLocked validates every conflict key before applying any registration.
// PendingTransaction.Commit holds the transaction mutex for this call.
func (r *Registry) commitLocked(pt *PendingTransaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, provider := range pt.providers {
		if _, exists := r.providers[provider.id]; exists {
			return fmt.Errorf("provider %q: duplicate ID", provider.id)
		}
	}
	for _, transformer := range pt.transformers {
		for _, existing := range r.transformers {
			if existing.pluginID == transformer.pluginID && existing.id == transformer.id {
				return fmt.Errorf("transformer %q for plugin %q: duplicate ID", transformer.id, transformer.pluginID)
			}
		}
	}
	for _, validator := range pt.validators {
		for _, committedValidators := range r.validators {
			for _, existing := range committedValidators {
				if existing.pluginID == validator.pluginID && existing.id == validator.id {
					return fmt.Errorf("validator %q for plugin %q: duplicate ID", validator.id, validator.pluginID)
				}
			}
		}
	}
	for _, observer := range pt.observers {
		for _, existing := range r.observers {
			if existing.pluginID == observer.pluginID && existing.id == observer.id {
				return fmt.Errorf("observer %q for plugin %q: duplicate ID", observer.id, observer.pluginID)
			}
		}
	}
	for _, handler := range pt.jobHandlers {
		if _, exists := r.jobHandlers[handler.jobType]; exists {
			return fmt.Errorf("job handler %q: duplicate job type", handler.jobType)
		}
	}
	for _, handler := range pt.eventHandlers {
		for _, existing := range r.eventHandlers {
			if existing.pluginID == handler.pluginID && existing.eventKey == handler.eventKey {
				return fmt.Errorf("event subscription %q for plugin %q: duplicate", handler.eventKey, handler.pluginID)
			}
		}
	}
	for _, command := range pt.commands {
		if _, exists := r.commands[command.id]; exists {
			return fmt.Errorf("command %q: duplicate ID", command.id)
		}
	}
	for _, ui := range pt.uis {
		if _, exists := r.uis[ui.id]; exists {
			return fmt.Errorf("UI %q: duplicate ID", ui.id)
		}
	}

	for _, provider := range pt.providers {
		r.providers[provider.id] = provider
	}
	r.transformers = append(r.transformers, pt.transformers...)
	sortTransformers(r.transformers)
	for _, validator := range pt.validators {
		r.validators[validator.capability] = append(r.validators[validator.capability], validator)
	}
	for capability := range r.validators {
		sortValidatorEntries(r.validators[capability])
	}
	r.observers = append(r.observers, pt.observers...)
	for _, handler := range pt.jobHandlers {
		r.jobHandlers[handler.jobType] = handler
	}
	r.eventHandlers = append(r.eventHandlers, pt.eventHandlers...)
	for _, command := range pt.commands {
		r.commands[command.id] = command
	}
	for _, ui := range pt.uis {
		r.uis[ui.id] = ui
	}
	return nil
}

// ResolveProvider resolves an explicit provider ID only; missing selections
// never fall back based on capability, priority, insertion order, or maps.
func (r *Registry) ResolveProvider(id string) (providerEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider, ok := r.providers[id]
	if !ok {
		return providerEntry{}, fmt.Errorf("provider %q: not found", id)
	}
	return provider, nil
}

func (r *Registry) ProviderCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.providers)
}

func (r *Registry) ListProviders() []providerEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	providers := make([]providerEntry, 0, len(r.providers))
	for _, provider := range r.providers {
		providers = append(providers, provider)
	}
	return providers
}

func (r *Registry) ListTransformers() []transformerEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	transformers := make([]transformerEntry, len(r.transformers))
	copy(transformers, r.transformers)
	return transformers
}

func (r *Registry) TransformerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.transformers)
}

func (r *Registry) ListValidators(capability v1.CapabilityID) []validatorEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if capability != "" {
		validators := make([]validatorEntry, len(r.validators[capability]))
		copy(validators, r.validators[capability])
		return validators
	}
	var validators []validatorEntry
	for _, entries := range r.validators {
		validators = append(validators, entries...)
	}
	sortValidatorEntries(validators)
	return validators
}

// Validate invokes every requested validator, preserving all findings and every
// execution error. Both results and errors are ordered by plugin and validator ID.
func (r *Registry) Validate(ctx context.Context, capability v1.CapabilityID, input v1.ImmutableBytes) ([]v1.ValidationResult, error) {
	validators := r.ListValidators(capability)
	var findings []v1.ValidationResult
	var validationErrors []error
	for _, validator := range validators {
		result, err := validator.validator.Validate(ctx, input)
		for _, finding := range result {
			finding.PluginID = validator.pluginID
			finding.ValidatorID = validator.id
			findings = append(findings, finding)
		}
		if err != nil {
			validationErrors = append(validationErrors,
				fmt.Errorf("validator %q (%s): %w", validator.id, validator.pluginID, err))
		}
	}
	sortValidationResults(findings)
	return findings, errors.Join(validationErrors...)
}

func (r *Registry) ValidatorCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, validators := range r.validators {
		count += len(validators)
	}
	return count
}

// DispatchEvent delivers only post-commit observers. An observer tuple is
// reserved while its callback is in flight and becomes completed only on success;
// a failed callback releases the reservation for a later retry.
func (r *Registry) DispatchEvent(ctx context.Context, event v1.Event) error {
	r.mu.Lock()
	observers := make([]observerEntry, len(r.observers))
	for i, observer := range r.observers {
		observers[i] = observer
		observers[i].eventTypes = cloneStrings(observer.eventTypes)
	}
	r.mu.Unlock()

	immutableEvent := event
	immutableEvent.Payload = v1.NewImmutableBytes(event.Payload.Bytes())
	for _, observer := range observers {
		if !matchesEventType(observer.eventTypes, immutableEvent.Type) {
			continue
		}
		tuple := observerTuple{pluginID: observer.pluginID, observerID: observer.id, eventID: immutableEvent.ID}
		if !r.reserveObserverDelivery(tuple) {
			continue
		}
		if err := observer.observer.OnEvent(ctx, immutableEvent); err != nil {
			r.releaseObserverDelivery(tuple)
			return fmt.Errorf("observer %q (%s): %w", observer.id, observer.pluginID, err)
		}
		r.completeObserverDelivery(tuple)
	}
	return nil
}

func matchesEventType(eventTypes []string, eventType string) bool {
	if len(eventTypes) == 0 {
		return true
	}
	for _, registeredType := range eventTypes {
		if registeredType == eventType {
			return true
		}
	}
	return false
}

func (r *Registry) reserveObserverDelivery(tuple observerTuple) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.completedEvents[tuple] || r.deliveringEvents[tuple] {
		return false
	}
	r.deliveringEvents[tuple] = true
	return true
}

func (r *Registry) releaseObserverDelivery(tuple observerTuple) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.deliveringEvents, tuple)
}

func (r *Registry) completeObserverDelivery(tuple observerTuple) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.deliveringEvents, tuple)
	r.completedEvents[tuple] = true
}

func (r *Registry) ObserverCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.observers)
}

func (r *Registry) ObservedEventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.completedEvents)
}

func (r *Registry) ResolveJobHandler(jobType string) (jobHandlerEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	handler, ok := r.jobHandlers[jobType]
	if !ok {
		return jobHandlerEntry{}, fmt.Errorf("job handler %q: not found", jobType)
	}
	return handler, nil
}

func (r *Registry) JobHandlerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobHandlers)
}

func (r *Registry) ListEventHandlers() []eventHandlerEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	handlers := make([]eventHandlerEntry, len(r.eventHandlers))
	for i, handler := range r.eventHandlers {
		handlers[i] = handler
		handlers[i].eventTypes = cloneStrings(handler.eventTypes)
	}
	return handlers
}

func (r *Registry) EventHandlerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.eventHandlers)
}

func (r *Registry) CommandCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.commands)
}

func (r *Registry) ListCommands() []commandEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	commands := make([]commandEntry, 0, len(r.commands))
	for _, command := range r.commands {
		commands = append(commands, command)
	}
	return commands
}

func (r *Registry) UICount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.uis)
}

func (r *Registry) ListUIs() []uiEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	uis := make([]uiEntry, 0, len(r.uis))
	for _, ui := range r.uis {
		uis = append(uis, ui)
	}
	return uis
}
