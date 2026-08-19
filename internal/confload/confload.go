// Package confload builds a typed configuration by layering, lowest to highest
// precedence: built-in defaults, an optional YAML file, and environment
// variables. It is shared by the server and manager binaries so both get
// identical layering, precedence, and env-override semantics.
package confload

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	env "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	koanf "github.com/knadh/koanf/v2"
)

// EnvPrefix scopes the environment variables that override configuration.
// Names are matched to config keys case-insensitively and ignoring
// underscores, so SEMANTIC__ENGINE__CONNECTION__HOST and
// SEMANTIC__ENGINE__CONNECTION__QUERY_TIMEOUT both resolve to their camelCase
// keys.
const EnvPrefix = "SEMANTIC__"

// Load merges three layers into a value of type T, lowest to highest
// precedence: defaults, an optional YAML file at path, and environment
// variables prefixed EnvPrefix. Fields are addressed by their yaml tags.
//
// Unknown keys are rejected: a YAML key or a prefixed environment variable that
// does not match a config field fails the load rather than being ignored, so a
// typo cannot silently fall back to a default. A string-slice field is settable
// from a single env var as a comma-separated list. Durations must carry a unit
// (for example "30s"); a bare number is rejected.
func Load[T any](defaults T, path string) (T, error) {
	var zero T

	keys, stringSlices := schema(reflect.TypeOf(defaults), "")
	envKeys, err := envKeyLookup(keys)
	if err != nil {
		return zero, err
	}

	k := koanf.New(".")

	// Layer 1: defaults, read from the defaults value using the yaml tags.
	if err := k.Load(structs.Provider(defaults, "yaml"), nil); err != nil {
		return zero, fmt.Errorf("loading defaults: %w", err)
	}

	// Layer 2: config file, if one was given. Unknown keys are rejected at
	// decode time by ErrorUnused below.
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return zero, fmt.Errorf("loading config file %q: %w", path, err)
		}
	}

	// Layer 3: environment overrides. Each prefixed variable is resolved to its
	// canonical key; a string-slice key takes a comma-separated value. Unknown
	// prefixed variables are collected and rejected after loading.
	var unknownEnv []string
	envProvider := env.Provider(".", env.Opt{
		Prefix: EnvPrefix,
		TransformFunc: func(name, value string) (string, any) {
			key := envKeys[normalizeKey(strings.TrimPrefix(name, EnvPrefix))]
			switch {
			case key == "":
				unknownEnv = append(unknownEnv, name)
				return "", nil
			case stringSlices[key]:
				return key, splitCSV(value)
			default:
				return key, value
			}
		},
	})
	if err := k.Load(envProvider, nil); err != nil {
		return zero, fmt.Errorf("loading environment: %w", err)
	}
	if len(unknownEnv) > 0 {
		sort.Strings(unknownEnv)
		return zero, fmt.Errorf("unknown %s environment variables: %s", EnvPrefix, strings.Join(unknownEnv, ", "))
	}

	var out T
	unmarshal := koanf.UnmarshalConf{
		Tag: "yaml",
		DecoderConfig: &mapstructure.DecoderConfig{
			WeaklyTypedInput: true,
			// Reject keys that map to no config field, at any depth (including
			// provider entries), so a typo in the file fails the load.
			ErrorUnused: true,
			DecodeHook:  mapstructure.ComposeDecodeHookFunc(durationHook),
		},
	}
	if err := k.UnmarshalWithConf("", &out, unmarshal); err != nil {
		return zero, fmt.Errorf("decoding configuration: %w", err)
	}
	return out, nil
}

// schema walks a struct type by its yaml tags and returns every leaf key in
// dotted form, plus the subset whose field is a []string (settable from a
// single comma-separated env var). It mirrors how the config maps to keys, so
// the caller knows the complete, authoritative key set without a running value.
func schema(rt reflect.Type, prefix string) (leaves []string, stringSlices map[string]bool) {
	stringSlices = map[string]bool{}
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			nested, nestedSlices := schema(ft, key)
			leaves = append(leaves, nested...)
			for k := range nestedSlices {
				stringSlices[k] = true
			}
		case ft.Kind() == reflect.Slice:
			leaves = append(leaves, key)
			if ft.Elem().Kind() == reflect.String {
				stringSlices[key] = true
			}
		default:
			leaves = append(leaves, key)
		}
	}
	return leaves, stringSlices
}

// envKeyLookup maps the normalized form of each config key to the key itself,
// so an environment variable resolves to its key regardless of case or
// underscores. It fails if two keys share a normalized form, which would make
// an environment override ambiguous.
func envKeyLookup(keys []string) (map[string]string, error) {
	lookup := make(map[string]string, len(keys))
	for _, key := range keys {
		n := normalizeKey(key)
		if existing, ok := lookup[n]; ok {
			return nil, fmt.Errorf("config keys %q and %q both normalize to %q for environment overrides; rename one", existing, key, n)
		}
		lookup[n] = key
	}
	return lookup, nil
}

// normalizeKey reduces a config key or environment variable name to a canonical
// form: uppercase letters and digits only, with every separator (dots, single
// and double underscores) dropped. So "engine.connection.host",
// "ENGINE__CONNECTION__HOST", and "ENGINE_CONNECTION_HOST" all normalize to
// "ENGINECONNECTIONHOST".
func normalizeKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitCSV splits a comma-separated env value into a slice, trimming blanks so a
// trailing comma or stray space cannot produce an empty element.
func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// durationHook decodes time.Duration fields strictly: a string must carry a
// unit (for example "30s"), and a bare number is rejected rather than being
// read as nanoseconds. Existing time.Duration values (from the defaults layer)
// pass through.
func durationHook(from, to reflect.Type, data any) (any, error) {
	durType := reflect.TypeOf(time.Duration(0))
	if to != durType {
		return data, nil
	}
	if from == durType {
		return data, nil
	}
	if from.Kind() == reflect.String {
		d, err := time.ParseDuration(data.(string))
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q (use a unit, e.g. \"30s\"): %w", data, err)
		}
		return d, nil
	}
	return nil, fmt.Errorf("duration must be a string with a unit like \"30s\", not %v", data)
}
