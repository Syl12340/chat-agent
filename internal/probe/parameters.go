package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This intentionally supports only the schema vocabulary our built-ins use.
// Unknown schema keywords fail registration. Business validators still own
// consent, revisions, timestamps, byte limits and cross-field constraints.
type parameterSchema struct {
	Type        json.RawMessage             `json:"type"`
	Description string                      `json:"description,omitempty"`
	Properties  map[string]*parameterSchema `json:"properties,omitempty"`
	Required    []string                    `json:"required,omitempty"`
	Additional  *bool                       `json:"additionalProperties,omitempty"`
	Enum        []json.RawMessage           `json:"enum,omitempty"`
	Minimum     *json.Number                `json:"minimum,omitempty"`
	Maximum     *json.Number                `json:"maximum,omitempty"`
	MaxLength   *int                        `json:"maxLength,omitempty"`
	MinLength   *int                        `json:"minLength,omitempty"`
	MinItems    *int                        `json:"minItems,omitempty"`
	MaxItems    *int                        `json:"maxItems,omitempty"`
	Items       *parameterSchema            `json:"items,omitempty"`
	types       []string
}

func parseParameterSchema(data json.RawMessage) (*parameterSchema, error) {
	if len(data) > 32<<10 {
		return nil, errors.New("tool schema too large")
	}
	var s parameterSchema
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return nil, errors.New("unsupported parameter schema")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid parameter schema")
	}
	if err := s.prepare(0); err != nil {
		return nil, err
	}
	if len(s.types) != 1 || s.types[0] != "object" {
		return nil, errors.New("tool parameters must be an object")
	}
	return &s, nil
}

func (s *parameterSchema) prepare(depth int) error {
	if s == nil || depth > 12 {
		return errors.New("invalid or too deeply nested schema")
	}
	var name string
	if json.Unmarshal(s.Type, &name) == nil {
		s.types = []string{name}
	} else if json.Unmarshal(s.Type, &s.types) != nil {
		return errors.New("schema type required")
	}
	if len(s.types) == 0 {
		return errors.New("empty schema type")
	}
	for _, t := range s.types {
		switch t {
		case "object", "array", "string", "integer", "number", "boolean", "null":
		default:
			return errors.New("unsupported schema type")
		}
	}
	for _, n := range []*int{s.MaxLength, s.MinLength, s.MinItems, s.MaxItems} {
		if n != nil && *n < 0 {
			return errors.New("negative schema limit")
		}
	}
	for _, p := range s.Properties {
		if err := p.prepare(depth + 1); err != nil {
			return err
		}
	}
	for _, key := range s.Required {
		if _, ok := s.Properties[key]; !ok {
			return errors.New("required property missing from schema")
		}
	}
	if s.Items != nil {
		return s.Items.prepare(depth + 1)
	}
	return nil
}

// decodeArguments rejects duplicate keys (including in nested objects), which
// encoding/json otherwise silently overwrites before a guard can see them.
func decodeArguments(raw []byte) (any, error) {
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return nil, errors.New("arguments too large or invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := decodeArgumentValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing argument data")
	}
	return v, nil
}

func decodeArgumentValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("arguments too deeply nested")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		if number, yes := t.(json.Number); yes {
			if _, valid := boundedNumber(number); !valid {
				return nil, errors.New("argument number out of supported range")
			}
		}
		return t, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, exists := out[name]; exists {
				return nil, errors.New("duplicate argument key")
			}
			v, err := decodeArgumentValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid argument object")
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			v, err := decodeArgumentValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid argument array")
		}
		return out, nil
	}
	return nil, errors.New("unexpected argument delimiter")
}

func (s *parameterSchema) validate(v any) error {
	kind := "null"
	switch v.(type) {
	case map[string]any:
		kind = "object"
	case []any:
		kind = "array"
	case string:
		kind = "string"
	case bool:
		kind = "boolean"
	case json.Number:
		kind = "number"
	}
	valid := false
	for _, t := range s.types {
		if t == kind {
			valid = true
		}
		if t == "integer" && kind == "number" {
			n, ok := boundedNumber(v.(json.Number))
			valid = valid || (ok && n.IsInt())
		}
	}
	if !valid {
		return errors.New("argument type does not match schema")
	}
	if len(s.Enum) > 0 {
		found := false
		for _, candidate := range s.Enum {
			value, err := decodeArguments(candidate)
			if err == nil && reflect.DeepEqual(value, v) {
				found = true
				break
			}
		}
		if !found {
			return errors.New("argument is not an allowed enum value")
		}
	}
	switch value := v.(type) {
	case map[string]any:
		for _, key := range s.Required {
			if _, ok := value[key]; !ok {
				return fmt.Errorf("missing required argument %s", key)
			}
		}
		for key, item := range value {
			child, ok := s.Properties[key]
			if !ok {
				if s.Additional == nil || !*s.Additional {
					return fmt.Errorf("unknown argument %s", key)
				}
				continue
			}
			if err := child.validate(item); err != nil {
				return fmt.Errorf("argument %s: %w", key, err)
			}
		}
	case []any:
		if (s.MinItems != nil && len(value) < *s.MinItems) || (s.MaxItems != nil && len(value) > *s.MaxItems) {
			return errors.New("argument array length out of range")
		}
		if s.Items != nil {
			for _, item := range value {
				if err := s.Items.validate(item); err != nil {
					return err
				}
			}
		}
	case string:
		n := utf8.RuneCountInString(value)
		if (s.MinLength != nil && n < *s.MinLength) || (s.MaxLength != nil && n > *s.MaxLength) {
			return errors.New("argument string length out of range")
		}
	case json.Number:
		n, ok := boundedNumber(value)
		if !ok {
			return errors.New("invalid argument number")
		}
		if s.Minimum != nil {
			bound, ok := boundedNumber(*s.Minimum)
			if !ok || n.Cmp(bound) < 0 {
				return errors.New("argument below minimum")
			}
		}
		if s.Maximum != nil {
			bound, ok := boundedNumber(*s.Maximum)
			if !ok || n.Cmp(bound) > 0 {
				return errors.New("argument above maximum")
			}
		}
	}
	return nil
}

// Keep arbitrary-precision parsing bounded even for adversarial exponents.
func boundedNumber(number json.Number) (*big.Rat, bool) {
	text := string(number)
	if len(text) > 128 {
		return nil, false
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(text[i+1:])
		if err != nil || exponent < -512 || exponent > 512 {
			return nil, false
		}
	}
	return new(big.Rat).SetString(text)
}
