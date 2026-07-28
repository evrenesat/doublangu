// Package v1 defines the versioned public plugin API for Doublangu.
// Plugins import only this package and must not depend on internal packages.
package v1

import "encoding/json"

// Language is a BCP-47 language tag (e.g. "nl", "en-US", "zh-Hant").
// The zero value is invalid; use the empty string only for "unspecified".
type Language string

// IsValid reports whether the tag is non-empty. Full BCP-47 structural
// validation is deferred to host-side handlers.
func (l Language) IsValid() bool { return l != "" }

// ImmutableBytes wraps a byte slice whose contents must not be mutated after
// construction. Callers must not retain or modify the backing array.
type ImmutableBytes struct{ b []byte }

// NewImmutableBytes copies src and returns an immutable wrapper.
func NewImmutableBytes(src []byte) ImmutableBytes {
	if len(src) == 0 {
		return ImmutableBytes{}
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return ImmutableBytes{b: dst}
}

// Bytes returns a copy of the underlying bytes. The caller owns the returned
// slice and may mutate it freely.
func (ib ImmutableBytes) Bytes() []byte {
	if len(ib.b) == 0 {
		return nil
	}
	dst := make([]byte, len(ib.b))
	copy(dst, ib.b)
	return dst
}

// Len returns the number of bytes.
func (ib ImmutableBytes) Len() int { return len(ib.b) }

// Equal reports whether ib and other contain the same bytes.
func (ib ImmutableBytes) Equal(other ImmutableBytes) bool {
	if len(ib.b) != len(other.b) {
		return false
	}
	for i := range ib.b {
		if ib.b[i] != other.b[i] {
			return false
		}
	}
	return true
}

// MarshalJSON encodes as a base64 string (standard encoding).
func (ib ImmutableBytes) MarshalJSON() ([]byte, error) {
	if ib.b == nil {
		return json.Marshal(nil)
	}
	return json.Marshal(ib.b)
}

// UnmarshalJSON decodes from a base64 string.
func (ib *ImmutableBytes) UnmarshalJSON(data []byte) error {
	var raw []byte
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*ib = NewImmutableBytes(raw)
	return nil
}

// Priority orders plugin handlers. Lower values run first (for transformers)
// or are preferred (for providers).
type Priority int

const (
	PriorityDefault Priority = 100
	PriorityHigh    Priority = 10
	PriorityLow     Priority = 1000
)
