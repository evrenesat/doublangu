// Package manifest implements the transactional plugin registry for Doublangu.
package manifest

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	v1 "doublangu/pkg/pluginapi/v1"
)

// transactionState makes a registration transaction terminal after either a
// successful commit or rollback. A failed registration is rolled back and its
// first error remains sticky so ignored Host errors cannot be committed later.
type transactionState uint8

const (
	transactionOpen transactionState = iota
	transactionCommitted
	transactionRolledBack
)

func (s transactionState) String() string {
	switch s {
	case transactionOpen:
		return "open"
	case transactionCommitted:
		return "committed"
	case transactionRolledBack:
		return "rolled back"
	default:
		return "unknown"
	}
}

// PendingTransaction accumulates registrations for exactly one Plugin.Register
// call. Its methods are safe for a retained Host to call concurrently, although
// plugin implementations must not retain the Host beyond Register.
type PendingTransaction struct {
	mu       sync.Mutex
	pluginID string
	registry *Registry
	state    transactionState
	err      error

	providers     []providerEntry
	transformers  []transformerEntry
	validators    []validatorEntry
	observers     []observerEntry
	jobHandlers   []jobHandlerEntry
	eventHandlers []eventHandlerEntry
	commands      []commandEntry
	uis           []uiEntry
}

// AddProvider adds a provider registration to the pending transaction.
func (pt *PendingTransaction) AddProvider(reg v1.ProviderRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.ID == "" {
		return pt.failLocked(fmt.Errorf("provider ID must not be empty"))
	}
	for _, existing := range pt.providers {
		if existing.id == reg.ID {
			return pt.failLocked(fmt.Errorf("provider %q: duplicate ID within transaction", reg.ID))
		}
	}
	pt.providers = append(pt.providers, providerEntry{
		pluginID: pt.pluginID, id: reg.ID, capability: reg.Capability, name: reg.Name,
		priority: reg.Priority, provider: reg.Provider,
	})
	return nil
}

// AddTransformer adds a transformer registration to the pending transaction.
func (pt *PendingTransaction) AddTransformer(reg v1.TransformerRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.ID == "" {
		return pt.failLocked(fmt.Errorf("transformer ID must not be empty"))
	}
	for _, existing := range pt.transformers {
		if existing.pluginID == pt.pluginID && existing.id == reg.ID {
			return pt.failLocked(fmt.Errorf("transformer %q: duplicate ID within transaction", reg.ID))
		}
	}
	pt.transformers = append(pt.transformers, transformerEntry{
		pluginID: pt.pluginID, id: reg.ID, capability: reg.Capability,
		priority: reg.Priority, transformer: reg.Transformer,
	})
	return nil
}

// AddValidator adds a validator registration to the pending transaction.
func (pt *PendingTransaction) AddValidator(reg v1.ValidatorRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.ID == "" {
		return pt.failLocked(fmt.Errorf("validator ID must not be empty"))
	}
	for _, existing := range pt.validators {
		if existing.pluginID == pt.pluginID && existing.id == reg.ID {
			return pt.failLocked(fmt.Errorf("validator %q: duplicate ID within transaction", reg.ID))
		}
	}
	pt.validators = append(pt.validators, validatorEntry{
		pluginID: pt.pluginID, id: reg.ID, capability: reg.Capability, validator: reg.Validator,
	})
	return nil
}

// AddObserver adds a post-commit observer registration to the pending transaction.
func (pt *PendingTransaction) AddObserver(reg v1.ObserverRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.ID == "" {
		return pt.failLocked(fmt.Errorf("observer ID must not be empty"))
	}
	for _, existing := range pt.observers {
		if existing.pluginID == pt.pluginID && existing.id == reg.ID {
			return pt.failLocked(fmt.Errorf("observer %q: duplicate ID within transaction", reg.ID))
		}
	}
	pt.observers = append(pt.observers, observerEntry{
		pluginID: pt.pluginID, id: reg.ID, eventTypes: cloneStrings(reg.EventTypes), observer: reg.Observer,
	})
	return nil
}

// AddJobHandler adds a durable job handler registration to the pending transaction.
func (pt *PendingTransaction) AddJobHandler(reg v1.JobHandlerRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.JobType == "" {
		return pt.failLocked(fmt.Errorf("job type must not be empty"))
	}
	for _, existing := range pt.jobHandlers {
		if existing.jobType == reg.JobType {
			return pt.failLocked(fmt.Errorf("job handler %q: duplicate job type within transaction", reg.JobType))
		}
	}
	pt.jobHandlers = append(pt.jobHandlers, jobHandlerEntry{
		pluginID: pt.pluginID, jobType: reg.JobType, handler: reg.Handler,
	})
	return nil
}

// AddEventHandler adds a canonical event subscription to the pending transaction.
func (pt *PendingTransaction) AddEventHandler(reg v1.EventHandlerRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	eventTypes, eventKey, err := canonicalEventTypes(reg.EventTypes)
	if err != nil {
		return pt.failLocked(err)
	}
	for _, existing := range pt.eventHandlers {
		if existing.pluginID == pt.pluginID && existing.eventKey == eventKey {
			return pt.failLocked(fmt.Errorf("event subscription %q: duplicate within transaction", eventKey))
		}
	}
	pt.eventHandlers = append(pt.eventHandlers, eventHandlerEntry{
		pluginID: pt.pluginID, eventTypes: eventTypes, eventKey: eventKey, handler: reg.Handler,
	})
	return nil
}

// AddCommand adds a command registration to the pending transaction.
func (pt *PendingTransaction) AddCommand(reg v1.CommandRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.ID == "" {
		return pt.failLocked(fmt.Errorf("command ID must not be empty"))
	}
	for _, existing := range pt.commands {
		if existing.id == reg.ID {
			return pt.failLocked(fmt.Errorf("command %q: duplicate ID within transaction", reg.ID))
		}
	}
	pt.commands = append(pt.commands, commandEntry{
		pluginID: pt.pluginID, id: reg.ID, label: reg.Label, description: reg.Description,
		category: reg.Category, handler: reg.Handler,
	})
	return nil
}

// AddUI adds a UI registration to the pending transaction.
func (pt *PendingTransaction) AddUI(reg v1.UIRegistration) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if reg.ID == "" {
		return pt.failLocked(fmt.Errorf("UI ID must not be empty"))
	}
	for _, existing := range pt.uis {
		if existing.id == reg.ID {
			return pt.failLocked(fmt.Errorf("UI %q: duplicate ID within transaction", reg.ID))
		}
	}
	pt.uis = append(pt.uis, uiEntry{
		pluginID: pt.pluginID, id: reg.ID, label: reg.Label, uiType: reg.Type,
		container: reg.Container, priority: reg.Priority, icon: reg.Icon, sourceURL: reg.SourceURL,
	})
	return nil
}

// Commit applies all accumulated registrations atomically. A failed or terminal
// transaction cannot publish registrations on a later call.
func (pt *PendingTransaction) Commit() error {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if err := pt.requireOpenLocked(); err != nil {
		return err
	}
	if err := pt.registry.commitLocked(pt); err != nil {
		return pt.failLocked(err)
	}
	pt.clearLocked()
	pt.state = transactionCommitted
	return nil
}

// Rollback discards all pending registrations. It is terminal: future Register*
// and Commit calls fail. A rollback after commit is harmless and cannot unpublish
// already committed state.
func (pt *PendingTransaction) Rollback() {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.state != transactionOpen {
		return
	}
	pt.clearLocked()
	pt.state = transactionRolledBack
}

func (pt *PendingTransaction) requireOpenLocked() error {
	if pt.err != nil {
		return pt.err
	}
	if pt.state != transactionOpen {
		return fmt.Errorf("registration transaction is %s", pt.state)
	}
	if pt.pluginID == "" {
		return pt.failLocked(fmt.Errorf("plugin ID must not be empty"))
	}
	return nil
}

func (pt *PendingTransaction) failLocked(err error) error {
	if pt.err == nil {
		pt.err = err
	}
	pt.clearLocked()
	pt.state = transactionRolledBack
	return pt.err
}

func (pt *PendingTransaction) clearLocked() {
	pt.providers = nil
	pt.transformers = nil
	pt.validators = nil
	pt.observers = nil
	pt.jobHandlers = nil
	pt.eventHandlers = nil
	pt.commands = nil
	pt.uis = nil
}

const allEventsKey = "\x00all-events"

func canonicalEventTypes(eventTypes []string) ([]string, string, error) {
	if len(eventTypes) == 0 {
		return nil, allEventsKey, nil
	}
	normalized := cloneStrings(eventTypes)
	sort.Strings(normalized)
	for i, eventType := range normalized {
		if eventType == "" {
			return nil, "", fmt.Errorf("event type must not be empty")
		}
		if i > 0 && eventType == normalized[i-1] {
			return nil, "", fmt.Errorf("event type %q: repeated in one registration", eventType)
		}
	}
	return normalized, "set:" + strings.Join(normalized, "\x00"), nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

// Entry types are private registry representation; registrations own copied
// mutable slices before they cross the transaction boundary.
type providerEntry struct {
	pluginID   string
	id         string
	capability v1.CapabilityID
	name       string
	priority   v1.Priority
	provider   interface{}
}

type transformerEntry struct {
	pluginID    string
	id          string
	capability  v1.CapabilityID
	priority    v1.Priority
	transformer v1.Transformer
}

type validatorEntry struct {
	pluginID   string
	id         string
	capability v1.CapabilityID
	validator  v1.Validator
}

type observerEntry struct {
	pluginID   string
	id         string
	eventTypes []string
	observer   v1.Observer
}

type jobHandlerEntry struct {
	pluginID string
	jobType  string
	handler  v1.JobHandler
}

type eventHandlerEntry struct {
	pluginID   string
	eventTypes []string
	eventKey   string
	handler    v1.EventHandler
}

type commandEntry struct {
	pluginID    string
	id          string
	label       string
	description string
	category    string
	handler     v1.CommandHandler
}

type uiEntry struct {
	pluginID  string
	id        string
	label     string
	uiType    v1.UIType
	container string
	priority  v1.Priority
	icon      string
	sourceURL string
}

func sortTransformers(entries []transformerEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		if entries[i].pluginID != entries[j].pluginID {
			return entries[i].pluginID < entries[j].pluginID
		}
		return entries[i].id < entries[j].id
	})
}

func sortValidatorEntries(entries []validatorEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].pluginID != entries[j].pluginID {
			return entries[i].pluginID < entries[j].pluginID
		}
		return entries[i].id < entries[j].id
	})
}

func sortValidationResults(results []v1.ValidationResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].PluginID != results[j].PluginID {
			return results[i].PluginID < results[j].PluginID
		}
		return results[i].ValidatorID < results[j].ValidatorID
	})
}
