// Package manifest provides schema-based manifest validation. It reads the
// checked-in JSON Schema, extracts validation rules, and applies them to raw
// manifest JSON. This package is the bridge between the JSON Schema contract and
// the Go type system: the parity test proves both paths agree on every fixture.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// SchemaPath is the filesystem path to the checked-in JSON Schema, relative to
// the module root. In production the file is distributed alongside the binary.
const SchemaPath = "contracts/plugin-manifest-v1.schema.json"

// moduleRoot returns the module root directory by locating go.mod from the
// current source file. Cached after first call.
func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot determine source file location")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// ParsedSchema holds the extracted validation rules from the JSON Schema.
type ParsedSchema struct {
	Required          []string
	Properties        map[string]PropertyRule
	AdditionalAllowed bool
}

// PropertyRule is one extracted property constraint.
type PropertyRule struct {
	Type        string   // "string" or "array"
	Pattern     string   // regex pattern (compiled lazily)
	MinLength   *int     // minimum string length
	Enum        []string // allowed enum values
	MinItems    *int
	UniqueItems bool
	Items       *PropertyRule // for array items
	OneOf       []PropertyRule
	Const       *string
}

// LoadSchema reads and parses the checked-in JSON Schema.
func LoadSchema() (*ParsedSchema, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, fmt.Errorf("find module root: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(root, SchemaPath))
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	return parseSchema(data)
}

// parseSchema extracts validation rules from a JSON Schema document.
func parseSchema(data []byte) (*ParsedSchema, error) {
	var raw struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type        string   `json:"type"`
			Pattern     string   `json:"pattern"`
			MinLength   *int     `json:"minLength"`
			Const       *string  `json:"const"`
			Enum        []string `json:"enum"`
			MinItems    *int     `json:"minItems"`
			UniqueItems bool     `json:"uniqueItems"`
			Items       *struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"items"`
			OneOf []struct {
				Const   *string `json:"const"`
				Type    string  `json:"type"`
				Pattern string  `json:"pattern"`
			} `json:"oneOf"`
		} `json:"properties"`
		AdditionalProperties interface{} `json:"additionalProperties"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse schema JSON: %w", err)
	}

	s := &ParsedSchema{
		Required:   raw.Required,
		Properties: make(map[string]PropertyRule, len(raw.Properties)),
	}

	// additionalProperties: false means reject unknown fields.
	if b, ok := raw.AdditionalProperties.(bool); ok && !b {
		s.AdditionalAllowed = false
	} else {
		s.AdditionalAllowed = true
	}

	for name, prop := range raw.Properties {
		rule := PropertyRule{
			Type:        prop.Type,
			Pattern:     prop.Pattern,
			MinLength:   prop.MinLength,
			Const:       prop.Const,
			Enum:        prop.Enum,
			MinItems:    prop.MinItems,
			UniqueItems: prop.UniqueItems,
		}
		if prop.Items != nil {
			rule.Items = &PropertyRule{
				Type: prop.Items.Type,
				Enum: prop.Items.Enum,
			}
		}
		for _, oo := range prop.OneOf {
			or := PropertyRule{
				Type:    oo.Type,
				Pattern: oo.Pattern,
				Const:   oo.Const,
			}
			rule.OneOf = append(rule.OneOf, or)
		}
		s.Properties[name] = rule
	}

	return s, nil
}

// Validate validates raw manifest JSON against the parsed schema rules.
// It returns nil on success or a structured error describing the first violation.
func (s *ParsedSchema) Validate(raw []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	// Check required fields.
	for _, req := range s.Required {
		v, ok := obj[req]
		if !ok {
			return fmt.Errorf("%s: required field is missing", req)
		}
		// Check for explicit null. json.RawMessage for null is "null".
		if strings.TrimSpace(string(v)) == "null" {
			return fmt.Errorf("%s: must not be null", req)
		}
	}

	// Check each present property.
	for key, rawVal := range obj {
		rule, ok := s.Properties[key]
		if !ok {
			if !s.AdditionalAllowed {
				return fmt.Errorf("%s: unknown field", key)
			}
			continue
		}

		// Check for null first.
		if strings.TrimSpace(string(rawVal)) == "null" {
			return fmt.Errorf("%s: must not be null", key)
		}

		if err := validateProperty(key, rawVal, rule); err != nil {
			return err
		}
	}

	return nil
}

func validateProperty(name string, raw json.RawMessage, rule PropertyRule) error {
	switch rule.Type {
	case "string":
		return validateStringProperty(name, raw, rule)
	case "array":
		return validateArrayProperty(name, raw, rule)
	case "":
		// No explicit type; validate via oneOf if present.
		if len(rule.OneOf) > 0 {
			return validateStringProperty(name, raw, rule)
		}
		return nil
	}
	return nil
}

func validateStringProperty(name string, raw json.RawMessage, rule PropertyRule) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	if rule.MinLength != nil && len(s) < *rule.MinLength {
		return fmt.Errorf("%s: must not be empty", name)
	}

	if rule.Pattern != "" {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return fmt.Errorf("%s: internal: invalid pattern %q: %w", name, rule.Pattern, err)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("%s: %q does not match required pattern", name, s)
		}
	}

	if len(rule.Enum) > 0 {
		found := false
		for _, e := range rule.Enum {
			if s == e {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: %q is not a valid value (allowed: %s)", name, s, strings.Join(rule.Enum, ", "))
		}
	}

	if rule.Const != nil && s != *rule.Const {
		return fmt.Errorf("%s: %q must equal %q", name, s, *rule.Const)
	}

	if len(rule.OneOf) > 0 {
		matched := false
		for _, or := range rule.OneOf {
			if or.Const != nil && s == *or.Const {
				matched = true
				break
			}
			if or.Pattern != "" {
				re, err := regexp.Compile(or.Pattern)
				if err != nil {
					continue
				}
				if re.MatchString(s) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return fmt.Errorf("%s: %q does not match any allowed alternative", name, s)
		}
	}

	return nil
}

func validateArrayProperty(name string, raw json.RawMessage, rule PropertyRule) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	if rule.MinItems != nil && len(arr) < *rule.MinItems {
		return fmt.Errorf("%s: must have at least %d items", name, *rule.MinItems)
	}

	if rule.UniqueItems {
		seen := make(map[string]bool, len(arr))
		for i, item := range arr {
			var s string
			if err := json.Unmarshal(item, &s); err != nil {
				return fmt.Errorf("%s[%d]: %w", name, i, err)
			}
			if seen[s] {
				return fmt.Errorf("%s[%d]: duplicate value %q", name, i, s)
			}
			seen[s] = true
		}
	}

	if rule.Items != nil {
		for i, item := range arr {
			if err := validateProperty(fmt.Sprintf("%s[%d]", name, i), item, *rule.Items); err != nil {
				return err
			}
		}
	}

	return nil
}
