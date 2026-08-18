package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization/opa"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization/ranger"
	rangerprovider "github.com/KubedAI/semantic-operator/internal/serving/authorization/ranger/provider"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	authorizationProvidersEnv     = "AUTHORIZATION_PROVIDERS"
	authorizationProvidersFileEnv = "AUTHORIZATION_PROVIDERS_FILE"
	authorizationTokenEnvPrefix   = "AUTHORIZATION_PROVIDER_TOKEN_"
	rangerServicePrincipalHeader  = "X-Forwarded-User"
)

var authorizationTokenEnvPattern = regexp.MustCompile(`^AUTHORIZATION_PROVIDER_TOKEN_[A-Z0-9_]+$`)

type authorizationProviderConfig struct {
	Name             string                             `json:"name"`
	Type             string                             `json:"type"`
	URL              string                             `json:"url"`
	TimeoutSeconds   int                                `json:"timeoutSeconds,omitempty"`
	BearerTokenEnv   string                             `json:"bearerTokenEnv,omitempty"`
	MaxResponseBytes int64                              `json:"maxResponseBytes,omitempty"`
	OPA              *opaAuthorizationProviderConfig    `json:"opa,omitempty"`
	Ranger           *rangerAuthorizationProviderConfig `json:"ranger,omitempty"`
}

type opaAuthorizationProviderConfig struct {
	DecisionPath string `json:"decisionPath"`
}

type rangerAuthorizationProviderConfig struct {
	AuthenticationMode string            `json:"authenticationMode"`
	ServicePrincipal   string            `json:"servicePrincipal"`
	AllowInsecureHTTP  bool              `json:"allowInsecureHTTP,omitempty"`
	ServiceType        string            `json:"serviceType"`
	ServiceName        string            `json:"serviceName"`
	Resource           string            `json:"resource"`
	Permission         string            `json:"permission"`
	ContextAttributes  map[string]string `json:"contextAttributes,omitempty"`
}

func authorizationRegistryFromEnv() (*authorization.Registry, error) {
	inline := strings.TrimSpace(os.Getenv(authorizationProvidersEnv))
	path := strings.TrimSpace(os.Getenv(authorizationProvidersFileEnv))
	if inline != "" && path != "" {
		return nil, fmt.Errorf("%s and %s are mutually exclusive", authorizationProvidersEnv, authorizationProvidersFileEnv)
	}
	if inline == "" && path == "" {
		return authorization.NewRegistry(), nil
	}

	raw := []byte(inline)
	source := authorizationProvidersEnv
	if path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s %q: %w", authorizationProvidersFileEnv, path, err)
		}
		source = fmt.Sprintf("%s %q", authorizationProvidersFileEnv, path)
	}
	return authorizationRegistryFromYAML(raw, source)
}

func authorizationRegistryFromYAML(raw []byte, source string) (*authorization.Registry, error) {
	decoder := yamlutil.NewYAMLToJSONDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("%s must contain a YAML array", source)
		}
		return nil, fmt.Errorf("decoding %s: %w", source, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decoding %s: %w", source, err)
		}
		return nil, fmt.Errorf("%s must contain exactly one YAML document", source)
	}

	var configs []authorizationProviderConfig
	if err := yaml.UnmarshalStrict(raw, &configs); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", source, err)
	}
	if configs == nil {
		return nil, fmt.Errorf("%s must contain a YAML array", source)
	}

	registry := authorization.NewRegistry()
	for i, cfg := range configs {
		if err := authorization.ValidateProviderName(cfg.Name); err != nil {
			return nil, fmt.Errorf("%s[%d].name: %w", source, i, err)
		}
		if cfg.URL == "" {
			return nil, fmt.Errorf("%s[%d].url is required", source, i)
		}
		if cfg.TimeoutSeconds < 0 || cfg.TimeoutSeconds > 30 {
			return nil, fmt.Errorf("%s[%d].timeoutSeconds must be between 0 and 30", source, i)
		}
		if cfg.MaxResponseBytes < 0 || cfg.MaxResponseBytes > 4<<20 {
			return nil, fmt.Errorf("%s[%d].maxResponseBytes must be between 0 and %d", source, i, 4<<20)
		}
		token := ""
		if cfg.BearerTokenEnv != "" {
			if !authorizationTokenEnvPattern.MatchString(cfg.BearerTokenEnv) {
				return nil, fmt.Errorf("authorization provider %q bearerTokenEnv %q must match %s",
					cfg.Name, cfg.BearerTokenEnv, authorizationTokenEnvPrefix+"[A-Z0-9_]+")
			}
			var ok bool
			token, ok = os.LookupEnv(cfg.BearerTokenEnv)
			if !ok || token == "" {
				return nil, fmt.Errorf("authorization provider %q requires non-empty environment variable %q", cfg.Name, cfg.BearerTokenEnv)
			}
		}
		timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
		var provider authorization.Provider
		var err error
		switch cfg.Type {
		case "opa":
			if cfg.OPA == nil {
				return nil, fmt.Errorf("%s[%d].opa is required when type is opa", source, i)
			}
			if cfg.Ranger != nil {
				return nil, fmt.Errorf("%s[%d].ranger is not allowed when type is opa", source, i)
			}
			if cfg.OPA.DecisionPath == "" {
				return nil, fmt.Errorf("%s[%d].opa.decisionPath is required", source, i)
			}
			provider, err = opa.New(opa.Options{
				URL: cfg.URL, DecisionPath: cfg.OPA.DecisionPath,
				BearerToken: token, Timeout: timeout,
				MaxResponseBytes: cfg.MaxResponseBytes,
			})
		case "ranger":
			if cfg.Ranger == nil {
				return nil, fmt.Errorf("%s[%d].ranger is required when type is ranger", source, i)
			}
			if cfg.OPA != nil {
				return nil, fmt.Errorf("%s[%d].opa is not allowed when type is ranger", source, i)
			}
			if cfg.Ranger.AuthenticationMode != "service" {
				return nil, fmt.Errorf("%s[%d].ranger.authenticationMode must be service", source, i)
			}
			if strings.TrimSpace(cfg.Ranger.ServicePrincipal) == "" {
				return nil, fmt.Errorf("%s[%d].ranger.servicePrincipal is required", source, i)
			}
			client, clientErr := ranger.New(ranger.Options{
				URL: cfg.URL, BearerToken: token,
				Headers:           map[string]string{rangerServicePrincipalHeader: cfg.Ranger.ServicePrincipal},
				AllowInsecureHTTP: cfg.Ranger.AllowInsecureHTTP,
				Timeout:           timeout,
				MaxResponseBytes:  cfg.MaxResponseBytes,
			})
			if clientErr != nil {
				err = clientErr
				break
			}
			provider, err = rangerprovider.New(client, rangerprovider.Options{
				ServiceType: cfg.Ranger.ServiceType, ServiceName: cfg.Ranger.ServiceName,
				ResourceTemplate: cfg.Ranger.Resource, Permission: cfg.Ranger.Permission,
				ContextAttributes: cfg.Ranger.ContextAttributes,
			})
		default:
			return nil, fmt.Errorf("%s[%d].type %q is not supported: use opa or ranger", source, i, cfg.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("configuring authorization provider %q: %w", cfg.Name, err)
		}
		if err := registry.Register(cfg.Name, provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
