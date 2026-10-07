package common_dto

import "sync"

// VariableDefinition is a single `variables` entry declared in a service's additional_config.
// It captures the raw expression plus the metadata needed for dependency-staged resolution:
// which service declared it (for ((self)) binding), the services/variables it references, and
// the earliest group boundary at which it can be resolved. Definitions are plain data; the
// resolution logic lives in the sequence_service package.
type VariableDefinition struct {
	Name           string   // variable name (registry key)
	Expression     string   // raw config expression, resolved by the standard placeholder engine
	OwnerServiceID int      // service whose additional_config declared this definition (for ((self)))
	RefServices    []string // service names referenced via ((ServiceName...)) (excludes self/var/item)
	RefVars        []string // variable names referenced via ((var.NAME...))
	RefSelf        bool     // true when the expression references ((self)) / ((self.path))
	ReadyGroup     int      // earliest group index at which all referenced services are terminal (-1 = pre-group-0)
	Resolved       bool     // set once this definition has been evaluated (regardless of empty/non-empty)
}

// VariableRegistry is the sequence-level bag of resolved variable values plus the pending
// definitions. Values are stored raw (interface{}) to preserve type. Writes happen only at
// group boundaries on a single goroutine; the mutex guards concurrent Snapshot reads made by
// parallel service goroutines while they build placeholder scopes.
type VariableRegistry struct {
	mu     sync.RWMutex
	values map[string]interface{}
	defs   []*VariableDefinition
}

// NewVariableRegistry creates an empty registry.
func NewVariableRegistry() *VariableRegistry {
	return &VariableRegistry{
		values: make(map[string]interface{}),
	}
}

// AddDefinition registers a definition (called during load-time aggregation).
func (r *VariableRegistry) AddDefinition(d *VariableDefinition) {
	if r == nil || d == nil {
		return
	}
	r.defs = append(r.defs, d)
}

// Definitions returns the aggregated definitions. Intended for single-threaded use during
// resolution (barrier stages); the slice itself is not copied.
func (r *VariableRegistry) Definitions() []*VariableDefinition {
	if r == nil {
		return nil
	}
	return r.defs
}

// HasDefinitions reports whether any variables were declared for this sequence.
func (r *VariableRegistry) HasDefinitions() bool {
	if r == nil {
		return false
	}
	return len(r.defs) > 0
}

// Get returns the resolved value for name and whether it is present.
func (r *VariableRegistry) Get(name string) (interface{}, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.values[name]
	return v, ok
}

// Has reports whether a resolved value exists for name.
func (r *VariableRegistry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// Set stores value for name (last write wins; merge policy is enforced by the caller).
func (r *VariableRegistry) Set(name string, value interface{}) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[name] = value
}

// Snapshot returns a shallow copy of the resolved values map, safe to read concurrently.
// Nested reference values are shared (not deep-copied); they are treated as read-only downstream.
func (r *VariableRegistry) Snapshot() map[string]interface{} {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.values) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out
}

// Len returns the number of resolved values.
func (r *VariableRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.values)
}
