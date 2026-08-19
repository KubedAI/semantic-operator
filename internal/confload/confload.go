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

// EnvPrefix scopes the environment variables that override configuration. The
// effective variable name is this prefix followed by a field's env tag, for
// example SEMANTIC__ENGINE_HOST for a field tagged env:"ENGINE_HOST".
const EnvPrefix = "SEMANTIC__"

// Load merges three layers into a value of type T, lowest to highest
// precedence: defaults, an optional YAML file at path, and environment
// variables prefixed EnvPrefix. YAML keys use the yaml tag; environment
// overrides are matched by the env tag.
//
// Unknown keys are rejected: a YAML key that maps to no field (at any depth,
// including provider entries) fails the load via ErrorUnused, and a prefixed
// environment variable whose name matches no env tag fails too. A []string
// field is settable from one comma-separated env value. Durations must carry a
// unit (for example "30s").
func Load[T any](defaults T, path string) (T, error) {
	var zero T

	envKeys, stringSlices, err := envTagSchema(reflect.TypeOf(defaults))
	if err != nil {
		return zero, err
	}

	k := koanf.New(".")

	// Layer 1: defaults, read from the defaults value using the yaml tags.
	if err := k.Load(structs.Provider(defaults, "yaml"), nil); err != nil {
		return zero, fmt.Errorf("loading defaults: %w", err)
	}

	// Layer 2: config file, if one was given.
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return zero, fmt.Errorf("loading config file %q: %w", path, err)
		}
	}

	// Layer 3: environment overrides, resolved by env tag to the yaml key. A
	// string-slice key takes a comma-separated value. Unknown prefixed
	// variables are collected and rejected after loading.
	var unknownEnv []string
	envProvider := env.Provider(".", env.Opt{
		Prefix: EnvPrefix,
		TransformFunc: func(name, value string) (string, any) {
			tag := strings.TrimPrefix(name, EnvPrefix)
			key, ok := envKeys[tag]
			if !ok {
				unknownEnv = append(unknownEnv, name)
				return "", nil
			}
			if stringSlices[key] {
				return key, splitCSV(value)
			}
			return key, value
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

// envTagSchema walks a struct, pairing each field's env tag with its dotted
// yaml key path. It returns the env-name -> key map and the set of keys whose
// field is a []string (settable from a single comma-separated env value). It
// fails if two fields share an env tag, which would make an override ambiguous.
// Fields without an env tag are simply not env-overridable.
func envTagSchema(rt reflect.Type) (map[string]string, map[string]bool, error) {
	envKeys := map[string]string{}
	stringSlices := map[string]bool{}
	var walk func(rt reflect.Type, prefix string) error
	walk = func(rt reflect.Type, prefix string) error {
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
			if ft.Kind() == reflect.Struct {
				if err := walk(ft, key); err != nil {
					return err
				}
				continue
			}
			envName, _, _ := strings.Cut(f.Tag.Get("env"), ",")
			if envName == "" {
				continue
			}
			if existing, dup := envKeys[envName]; dup {
				return fmt.Errorf("env tag %q is used by both %q and %q; give each field a unique env tag", envName, existing, key)
			}
			envKeys[envName] = key
			if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String {
				stringSlices[key] = true
			}
		}
		return nil
	}
	if err := walk(rt, ""); err != nil {
		return nil, nil, err
	}
	return envKeys, stringSlices, nil
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
