package schema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/sathyassn/agent-workstation-kit/internal/engine"
)

// Layer identifies one configuration precedence layer in SPC-001 order:
// kit defaults -> fleet -> machine -> user -> environment -> flags.
type Layer string

// Configuration layers from lowest to highest precedence, plus LayerSafetyFloor
// for non-configurable invariants.
const (
	LayerKitDefault  Layer = "kit-default"
	LayerFleet       Layer = "fleet"
	LayerMachine     Layer = "machine"
	LayerUser        Layer = "user"
	LayerEnv         Layer = "env"
	LayerFlag        Layer = "flag"
	LayerSafetyFloor Layer = "safety-floor"
)

// AllConfigurableLayers returns the six configuration layers in precedence
// order (lowest to highest).
func AllConfigurableLayers() []Layer {
	return []Layer{
		LayerKitDefault,
		LayerFleet,
		LayerMachine,
		LayerUser,
		LayerEnv,
		LayerFlag,
	}
}

// AllLayers returns every Layer including LayerSafetyFloor.
func AllLayers() []Layer {
	return append(AllConfigurableLayers(), LayerSafetyFloor)
}

// String returns the canonical layer name.
func (l Layer) String() string { return string(l) }

// Valid reports whether l is a known Layer.
func (l Layer) Valid() bool {
	return slices.Contains(AllLayers(), l)
}

const (
	envPrefix         = "WSKIT_"
	safetySectionName = "safety"
	safetyKeyPrefix   = safetySectionName + "."
	safetyValueText   = "enforced"
	flagSourceLabel   = "--set"
	kitSourceLabel    = "schema/profile.toml"
	boolTrueStr       = "true"
	boolFalseStr      = "false"
)

// EnvVarForKey returns the environment variable name mapped to k, for example
// WSKIT_BACKUP_RETENTION_DAYS for backup.retention_days.
func EnvVarForKey(k Key) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(k.String(), ".", "_"))
}

// FlagOverride is one CLI --set KEY=VALUE override.
type FlagOverride struct {
	Key      string
	RawValue string
}

// LayerSource provides a decoded TOML map and its human-readable source label
// (such as a file path) for a file-backed configuration layer.
type LayerSource struct {
	Path string
	Data map[string]any
}

// ResolveOptions supplies the inputs for each configuration layer.
type ResolveOptions struct {
	Fleet        *LayerSource
	Machine      *LayerSource
	User         *LayerSource
	Getenv       func(string) string
	Flags        []FlagOverride
	KitOverrides map[string]any
}

// LayerEntry records one layer's contribution to a key's resolution history.
type LayerEntry struct {
	Layer  Layer  `json:"layer"`
	Value  any    `json:"value"`
	Source string `json:"source,omitempty"`
}

// ResolvedValue is the effective value of one key together with its winning
// layer and full layer history.
type ResolvedValue struct {
	Key        Key          `json:"key"`
	Value      any          `json:"value"`
	Layer      Layer        `json:"layer"`
	Source     string       `json:"source,omitempty"`
	IsOverride bool         `json:"is_override"`
	History    []LayerEntry `json:"history,omitempty"`
}

// ResolvedConfig holds the merged configuration across all six layers with
// provenance for every key.
type ResolvedConfig struct {
	values map[Key]ResolvedValue
	order  []Key
}

// FieldExplanation is the structured output of `wskit config explain KEY`.
type FieldExplanation struct {
	SchemaVersion    int          `json:"schema_version"`
	Key              Key          `json:"key"`
	Configurable     bool         `json:"configurable"`
	Rule             SafetyRule   `json:"rule,omitempty"`
	Value            any          `json:"value"`
	HasValue         bool         `json:"has_value"`
	Layer            Layer        `json:"layer"`
	Source           string       `json:"source,omitempty"`
	Default          any          `json:"default,omitempty"`
	HasDefault       bool         `json:"has_default"`
	IsOverride       bool         `json:"is_override"`
	EnvVar           string       `json:"env_var,omitempty"`
	Stage            string       `json:"stage,omitempty"`
	What             string       `json:"what"`
	Why              string       `json:"why"`
	Options          []string     `json:"options,omitempty"`
	Recommended      string       `json:"recommended,omitempty"`
	Risk             string       `json:"risk"`
	Undo             string       `json:"undo"`
	ManualEquivalent string       `json:"manual_equivalent,omitempty"`
	Layers           []LayerEntry `json:"layers,omitempty"`
}

// CheckSafetyFloor verifies that setting rawKey to val in layer does not
// violate any safety-floor rule. Non-configurable safety.* keys are refused in
// every layer regardless of value; baseline security invariants are refused if
// weakened in any layer.
func CheckSafetyFloor(layer Layer, rawKey string, val any) error {
	key := Key(rawKey)
	if spec, ok := LookupSafetyFloor(key); ok {
		if !spec.HasRequiredValue {
			return &SafetyFloorError{
				Key:     key,
				Layer:   layer,
				Rule:    spec.Rule,
				Message: spec.What + " " + spec.Undo,
			}
		}
		if !valuesEqual(val, spec.RequiredValue) {
			return &SafetyFloorError{
				Key:     key,
				Layer:   layer,
				Rule:    spec.Rule,
				Message: fmt.Sprintf("must remain %v (%s)", spec.RequiredValue, spec.Risk),
			}
		}
		return nil
	}
	if strings.HasPrefix(rawKey, safetyKeyPrefix) || rawKey == safetySectionName {
		return &SafetyFloorError{
			Key:     key,
			Layer:   layer,
			Rule:    RuleSafetyFloorUnknown,
			Message: "keys under safety.* are part of the safety floor and cannot be configured",
		}
	}
	return nil
}

// EnvOverride is one parsed value coming from a WSKIT_* environment variable.
type EnvOverride struct {
	Key    Key
	Value  any
	EnvVar string
}

// ResolveEnvLayer inspects getenv for safety-floor and schema WSKIT_*
// environment variables, refusing safety-floor violations and invalid types
// using the exact same rules as Resolve.
func ResolveEnvLayer(getenv func(string) string) ([]EnvOverride, error) {
	if getenv == nil {
		return nil, nil
	}
	// Check non-configurable safety-floor env vars first so any attempt is
	// refused regardless of value.
	for _, sf := range SafetyFloorSpecs() {
		if sf.HasRequiredValue {
			continue
		}
		envName := EnvVarForKey(sf.Key)
		if rawVal := getenv(envName); rawVal != "" {
			if err := CheckSafetyFloor(LayerEnv, sf.Key.String(), rawVal); err != nil {
				return nil, err
			}
		}
	}
	var out []EnvOverride
	for _, fs := range FieldSpecs() {
		envName := EnvVarForKey(fs.Key)
		rawVal := getenv(envName)
		if rawVal == "" {
			continue
		}
		parsed, err := ParseValueForSpec(fs, rawVal)
		if err != nil {
			if sfErr := CheckSafetyFloor(LayerEnv, fs.Key.String(), rawVal); sfErr != nil {
				return nil, sfErr
			}
			return nil, fmt.Errorf("env %s: %w", envName, err)
		}
		if err := CheckSafetyFloor(LayerEnv, fs.Key.String(), parsed); err != nil {
			return nil, err
		}
		if err := ValidateValueType(fs, parsed); err != nil {
			return nil, fmt.Errorf("env %s: %w", envName, err)
		}
		out = append(out, EnvOverride{Key: fs.Key, Value: parsed, EnvVar: envName})
	}
	return out, nil
}

// ValidateValueType verifies that val matches spec's declared schema type and
// enum membership without coercing mismatched types.
func ValidateValueType(spec FieldSpec, val any) error {
	switch spec.Type {
	case FieldString:
		if _, ok := val.(string); !ok {
			return fmt.Errorf("%w: %s must be a string", ErrInvalidValue, spec.Key)
		}
		return nil
	case FieldEnum:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("%w: %s must be a string from %v", ErrInvalidValue, spec.Key, spec.EnumValues)
		}
		if !slices.Contains(spec.EnumValues, s) {
			return fmt.Errorf("%w: %s must be one of %v, got %s", ErrInvalidValue, spec.Key, spec.EnumValues, redactRawForError(s))
		}
		return nil
	case FieldBool:
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("%w: %s must be a boolean", ErrInvalidValue, spec.Key)
		}
		return nil
	case FieldInt:
		if _, ok := val.(int64); !ok {
			return fmt.Errorf("%w: %s must be an integer", ErrInvalidValue, spec.Key)
		}
		return nil
	case FieldFloat:
		if _, ok := asFiniteNumber(val); !ok {
			return fmt.Errorf("%w: %s must be a finite number", ErrInvalidValue, spec.Key)
		}
		return nil
	case FieldStringList:
		if _, ok := asStringSlice(val); !ok {
			return fmt.Errorf("%w: %s must be a list of strings", ErrInvalidValue, spec.Key)
		}
		return nil
	case FieldStringMap:
		if _, ok := asStringMap(val); !ok {
			return fmt.Errorf("%w: %s must be a table of string values", ErrInvalidValue, spec.Key)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported field type %q", ErrInvalidValue, spec.Type)
	}
}

// Resolve merges all six configuration layers in precedence order
// (kit-default -> fleet -> machine -> user -> env -> flag), refusing any
// safety-floor violation, duplicate key or type mismatch in any layer before
// returning.
func Resolve(opts ResolveOptions) (*ResolvedConfig, error) {
	specs := FieldSpecs()
	specsByKey := make(map[Key]FieldSpec, len(specs))
	order := make([]Key, 0, len(specs))
	values := make(map[Key]ResolvedValue, len(specs))

	for _, fs := range specs {
		specsByKey[fs.Key] = fs
		order = append(order, fs.Key)
		if fs.HasDefault {
			if err := CheckSafetyFloor(LayerKitDefault, fs.Key.String(), fs.Default); err != nil {
				return nil, err
			}
			if err := ValidateValueType(fs, fs.Default); err != nil {
				return nil, fmt.Errorf("layer %s (%s): %w", LayerKitDefault, kitSourceLabel, err)
			}
			entry := LayerEntry{Layer: LayerKitDefault, Value: cloneValue(fs.Default), Source: kitSourceLabel}
			values[fs.Key] = ResolvedValue{
				Key:     fs.Key,
				Value:   cloneValue(fs.Default),
				Layer:   LayerKitDefault,
				Source:  kitSourceLabel,
				History: []LayerEntry{entry},
			}
		}
	}

	apply := func(layer Layer, rawKey string, val any, source string) error {
		if err := CheckSafetyFloor(layer, rawKey, val); err != nil {
			return err
		}
		k := Key(rawKey)
		spec, ok := specsByKey[k]
		if !ok {
			return fmt.Errorf("%w: %q in layer %s", ErrUnknownKey, rawKey, layer)
		}
		if err := ValidateValueType(spec, val); err != nil {
			return fmt.Errorf("layer %s (%s): %w", layer, source, err)
		}
		rv := values[k]
		rv.Key = k
		rv.Value = cloneValue(val)
		rv.Layer = layer
		rv.Source = source
		rv.History = append(rv.History, LayerEntry{
			Layer:  layer,
			Value:  cloneValue(val),
			Source: source,
		})
		rv.IsOverride = !spec.HasDefault || !valuesEqual(val, spec.Default)
		values[k] = rv
		return nil
	}

	// Optional kit-override map (primarily to test safety-floor enforcement at
	// the lowest layer).
	if len(opts.KitOverrides) > 0 {
		flat, err := flattenTOMLMap(opts.KitOverrides)
		if err != nil {
			return nil, fmt.Errorf("layer %s (%s): %w", LayerKitDefault, kitSourceLabel, err)
		}
		for _, fk := range sortedFlatKeys(flat) {
			if err := apply(LayerKitDefault, fk, flat[fk], kitSourceLabel); err != nil {
				return nil, err
			}
		}
	}

	// File layers: fleet -> machine -> user.
	for _, fl := range []struct {
		layer Layer
		src   *LayerSource
	}{
		{layer: LayerFleet, src: opts.Fleet},
		{layer: LayerMachine, src: opts.Machine},
		{layer: LayerUser, src: opts.User},
	} {
		if fl.src == nil || len(fl.src.Data) == 0 {
			continue
		}
		flat, err := flattenTOMLMap(fl.src.Data)
		if err != nil {
			return nil, fmt.Errorf("layer %s (%s): %w", fl.layer, fl.src.Path, err)
		}
		for _, fk := range sortedFlatKeys(flat) {
			if err := apply(fl.layer, fk, flat[fk], fl.src.Path); err != nil {
				return nil, err
			}
		}
	}

	// Environment layer.
	envOverrides, err := ResolveEnvLayer(opts.Getenv)
	if err != nil {
		return nil, err
	}
	for _, ov := range envOverrides {
		if err := apply(LayerEnv, ov.Key.String(), ov.Value, ov.EnvVar); err != nil {
			return nil, err
		}
	}

	// Flag layer (--set key=value).
	for _, fl := range opts.Flags {
		rawKey := strings.TrimSpace(fl.Key)
		if err := CheckSafetyFloor(LayerFlag, rawKey, fl.RawValue); err != nil {
			// For required-value safety keys, try typed parsing before refusing
			// so `--set security.disk_encryption_required=true` is accepted.
			spec, isField := specsByKey[Key(rawKey)]
			if !isField {
				return nil, err
			}
			parsed, parseErr := ParseValueForSpec(spec, fl.RawValue)
			if parseErr != nil {
				return nil, err
			}
			if err2 := CheckSafetyFloor(LayerFlag, rawKey, parsed); err2 != nil {
				return nil, err2
			}
			if err3 := apply(LayerFlag, rawKey, parsed, flagSourceLabel); err3 != nil {
				return nil, err3
			}
			continue
		}
		spec, ok := specsByKey[Key(rawKey)]
		if !ok {
			return nil, fmt.Errorf("%w: %q in layer %s", ErrUnknownKey, rawKey, LayerFlag)
		}
		parsed, err := ParseValueForSpec(spec, fl.RawValue)
		if err != nil {
			return nil, fmt.Errorf("flag --set %s: %w", rawKey, err)
		}
		if err := apply(LayerFlag, rawKey, parsed, flagSourceLabel); err != nil {
			return nil, err
		}
	}

	return &ResolvedConfig{values: values, order: order}, nil
}

// Get returns the resolved value and layer for k.
func (r *ResolvedConfig) Get(k Key) (ResolvedValue, bool) {
	rv, ok := r.values[k]
	if !ok {
		return ResolvedValue{}, false
	}
	rv.Value = cloneValue(rv.Value)
	rv.History = slices.Clone(rv.History)
	return rv, true
}

// All returns every resolved value in schema declaration order.
func (r *ResolvedConfig) All() []ResolvedValue {
	out := make([]ResolvedValue, 0, len(r.values))
	for _, k := range r.order {
		if rv, ok := r.Get(k); ok {
			out = append(out, rv)
		}
	}
	return out
}

// ToRawMap reconstructs a nested TOML-shaped map from all resolved values.
func (r *ResolvedConfig) ToRawMap() map[string]any {
	doc := make(map[string]any)
	for _, k := range r.order {
		rv, ok := r.values[k]
		if !ok {
			continue
		}
		SetValueInMap(doc, k, cloneValue(rv.Value))
	}
	return doc
}

// ToExplicitRawMap reconstructs a nested TOML-shaped map containing only keys
// explicitly supplied by a non-default layer (fleet, machine, user, env or
// flag), so kit defaults never mask missing required Schema 3 keys during
// layered validation.
func (r *ResolvedConfig) ToExplicitRawMap() map[string]any {
	doc := make(map[string]any)
	for _, k := range r.order {
		rv, ok := r.values[k]
		if !ok || rv.Layer == LayerKitDefault {
			continue
		}
		SetValueInMap(doc, k, cloneValue(rv.Value))
	}
	return doc
}

// Explain builds a full explanation for k combining schema metadata, safety
// floor status, the effective value, its winning layer and shadowed layers.
func (r *ResolvedConfig) Explain(k Key) (FieldExplanation, error) {
	if sf, ok := LookupSafetyFloor(k); ok && !sf.HasRequiredValue {
		return FieldExplanation{
			SchemaVersion: 1,
			Key:           k,
			Configurable:  false,
			Rule:          sf.Rule,
			Value:         safetyValueText,
			HasValue:      true,
			Layer:         LayerSafetyFloor,
			Source:        kitSourceLabel,
			What:          sf.What,
			Why:           sf.Why,
			Risk:          sf.Risk,
			Undo:          sf.Undo,
		}, nil
	}

	spec, ok := LookupField(k)
	if !ok {
		return FieldExplanation{}, fmt.Errorf("%w: %q", ErrUnknownKey, k)
	}

	exp := FieldExplanation{
		SchemaVersion:    1,
		Key:              spec.Key,
		Configurable:     true,
		HasDefault:       spec.HasDefault,
		Default:          cloneValue(spec.Default),
		EnvVar:           EnvVarForKey(spec.Key),
		Stage:            spec.Stage,
		What:             spec.What,
		Why:              spec.Why,
		Options:          slices.Clone(spec.Options),
		Recommended:      spec.Recommended,
		Risk:             spec.Risk,
		Undo:             spec.Undo,
		ManualEquivalent: spec.ManualEquivalent,
	}
	if sf, ok := LookupSafetyFloor(k); ok {
		exp.Rule = sf.Rule
	}
	if rv, found := r.Get(k); found {
		exp.Value = rv.Value
		exp.HasValue = true
		exp.Layer = rv.Layer
		exp.Source = rv.Source
		exp.IsOverride = rv.IsOverride
		exp.Layers = rv.History
	}
	return exp, nil
}

// ParseValueForKey parses a string value for key k using its declared schema
// type and checks the safety floor.
func ParseValueForKey(layer Layer, rawKey, rawVal string) (any, error) {
	key := Key(rawKey)
	if sf, ok := LookupSafetyFloor(key); ok && !sf.HasRequiredValue {
		return nil, CheckSafetyFloor(layer, rawKey, rawVal)
	}
	if strings.HasPrefix(rawKey, safetyKeyPrefix) || rawKey == safetySectionName {
		return nil, CheckSafetyFloor(layer, rawKey, rawVal)
	}
	spec, ok := LookupField(key)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, rawKey)
	}
	parsed, err := ParseValueForSpec(spec, rawVal)
	if err != nil {
		if sfErr := CheckSafetyFloor(layer, rawKey, rawVal); sfErr != nil {
			return nil, sfErr
		}
		return nil, err
	}
	if err := CheckSafetyFloor(layer, rawKey, parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// ParseValueForSpec parses a raw CLI or environment string according to spec.
func ParseValueForSpec(spec FieldSpec, raw string) (any, error) {
	switch spec.Type {
	case FieldString:
		return raw, nil
	case FieldEnum:
		if !slices.Contains(spec.EnumValues, raw) {
			return nil, fmt.Errorf("%w: %s must be one of %v, got %s", ErrInvalidValue, spec.Key, spec.EnumValues, redactRawForError(raw))
		}
		return raw, nil
	case FieldBool:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case boolTrueStr:
			return true, nil
		case boolFalseStr:
			return false, nil
		default:
			return nil, fmt.Errorf("%w: %s must be %s or %s, got %s", ErrInvalidValue, spec.Key, boolTrueStr, boolFalseStr, redactRawForError(raw))
		}
	case FieldInt:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			if LooksLikeSecret(raw) {
				return nil, fmt.Errorf("%w: %s must be an integer, got %s", ErrInvalidValue, spec.Key, RedactText)
			}
			return nil, fmt.Errorf("%w: %s must be an integer: %w", ErrInvalidValue, spec.Key, err)
		}
		return n, nil
	case FieldFloat:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			if LooksLikeSecret(raw) {
				return nil, fmt.Errorf("%w: %s must be a number, got %s", ErrInvalidValue, spec.Key, RedactText)
			}
			return nil, fmt.Errorf("%w: %s must be a number: %w", ErrInvalidValue, spec.Key, err)
		}
		return f, nil
	case FieldStringList:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "[]" {
			return []string{}, nil
		}
		if strings.HasPrefix(trimmed, "[") {
			var list []string
			if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
				return nil, fmt.Errorf("%w: %s invalid JSON string list: %w", ErrInvalidValue, spec.Key, err)
			}
			return list, nil
		}
		parts := strings.Split(trimmed, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out, nil
	case FieldStringMap:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "{}" {
			return map[string]string{}, nil
		}
		if strings.HasPrefix(trimmed, "{") {
			var m map[string]string
			if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
				return nil, fmt.Errorf("%w: %s invalid JSON string map: %w", ErrInvalidValue, spec.Key, err)
			}
			return m, nil
		}
		m := make(map[string]string)
		for _, pair := range strings.Split(trimmed, ",") {
			k, v, ok := strings.Cut(pair, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("%w: %s map entry %s must be key=value", ErrInvalidValue, spec.Key, redactRawForError(pair))
			}
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		return m, nil
	default:
		return nil, fmt.Errorf("%w: unsupported field type %q", ErrInvalidValue, spec.Type)
	}
}

// SetValueInMap inserts val at dotted key k inside doc, returning the previous
// value (if any). If a legacy quoted dotted top-level key existed for k, it is
// replaced by the canonical nested table entry so duplicates are never created.
func SetValueInMap(doc map[string]any, k Key, val any) (any, bool) {
	sec, name, hasSec := strings.Cut(k.String(), ".")
	if !hasSec {
		old, had := doc[k.String()]
		doc[k.String()] = val
		return old, had
	}
	quotedOld, hadQuoted := doc[k.String()]
	if hadQuoted {
		delete(doc, k.String())
	}
	tbl := asTable(doc[sec])
	if tbl == nil {
		tbl = make(map[string]any)
		doc[sec] = tbl
	}
	old, had := tbl[name]
	tbl[name] = val
	if !had && hadQuoted {
		return quotedOld, true
	}
	return old, had
}

// AtomicWriteFile writes data to path via a synced temporary file in the same
// directory followed by an atomic rename and parent-directory fsync. When path
// already exists as a regular file, its owner, mode, extended attributes and
// ACLs are preserved; symlinks and non-regular targets are refused.
func AtomicWriteFile(path string, data []byte, perm fs.FileMode) error {
	if err := engine.ReplaceFilePreservingMetadata(path, data, perm); err != nil {
		return fmt.Errorf("atomic write %s: %w", path, err)
	}
	return nil
}

// CheckNoDuplicateKeys verifies that doc does not define the same dotted key
// in both quoted top-level form and nested table form.
func CheckNoDuplicateKeys(doc map[string]any) error {
	_, err := flattenTOMLMap(doc)
	return err
}

// CheckRawSafetyFloor verifies that a raw TOML document contains no duplicate
// quoted/nested keys and violates no safety-floor rule in layer.
func CheckRawSafetyFloor(layer Layer, doc map[string]any) error {
	flat, err := flattenTOMLMap(doc)
	if err != nil {
		return err
	}
	for _, fk := range sortedFlatKeys(flat) {
		if sfErr := CheckSafetyFloor(layer, fk, flat[fk]); sfErr != nil {
			return sfErr
		}
	}
	return nil
}

// flattenTOMLMap flattens a decoded TOML document into dotted keys, keeping
// string-map fields (like accounts.admin_assignments) as whole leaf values,
// and refusing any duplicate between quoted dotted keys and nested tables.
func flattenTOMLMap(doc map[string]any) (map[string]any, error) {
	out := make(map[string]any)
	topKeys := sortedFlatKeys(doc)
	for _, k := range topKeys {
		v := doc[k]
		tbl := asTable(v)
		if tbl == nil {
			if _, exists := out[k]; exists {
				return nil, fmt.Errorf("%w: duplicate key %q defined both as quoted dotted key and nested table", ErrInvalidValue, k)
			}
			out[k] = v
			continue
		}
		if len(tbl) == 0 {
			if k == safetySectionName || !Section(k).Valid() {
				if _, exists := out[k]; exists {
					return nil, fmt.Errorf("%w: duplicate key %q defined both as quoted dotted key and nested table", ErrInvalidValue, k)
				}
				out[k] = v
			}
			continue
		}
		for _, subK := range sortedFlatKeys(tbl) {
			dotted := k + "." + subK
			if _, exists := out[dotted]; exists {
				return nil, fmt.Errorf("%w: duplicate key %q defined both as quoted dotted key and nested table", ErrInvalidValue, dotted)
			}
			out[dotted] = tbl[subK]
		}
	}
	return out, nil
}

func sortedFlatKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func valuesEqual(a, b any) bool {
	if sA, okA := asStringSlice(a); okA {
		if sB, okB := asStringSlice(b); okB {
			return slices.Equal(sA, sB)
		}
	}
	if mA, okA := asStringMap(a); okA {
		if mB, okB := asStringMap(b); okB {
			return reflect.DeepEqual(mA, mB)
		}
	}
	return reflect.DeepEqual(a, b)
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case []string:
		return slices.Clone(x)
	case []any:
		return slices.Clone(x)
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, val := range x {
			out[k] = val
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = cloneValue(val)
		}
		return out
	default:
		return v
	}
}
