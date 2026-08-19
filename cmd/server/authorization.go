package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization/opa"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization/ranger"
	rangerprovider "github.com/KubedAI/semantic-operator/internal/serving/authorization/ranger/provider"
)

const (
	authorizationTokenEnvPrefix  = "AUTHORIZATION_PROVIDER_TOKEN_"
	rangerServicePrincipalHeader = "X-Forwarded-User"
)

var authorizationTokenEnvPattern = regexp.MustCompile(`^AUTHORIZATION_PROVIDER_TOKEN_[A-Z0-9_]+$`)

type authorizationProviderConfig struct {
	Name             string                             `json:"name" yaml:"name"`
	Type             string                             `json:"type" yaml:"type"`
	URL              string                             `json:"url" yaml:"url"`
	TimeoutSeconds   int                                `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
	BearerTokenEnv   string                             `json:"bearerTokenEnv,omitempty" yaml:"bearerTokenEnv,omitempty"`
	MaxResponseBytes int64                              `json:"maxResponseBytes,omitempty" yaml:"maxResponseBytes,omitempty"`
	OPA              *opaAuthorizationProviderConfig    `json:"opa,omitempty" yaml:"opa,omitempty"`
	Ranger           *rangerAuthorizationProviderConfig `json:"ranger,omitempty" yaml:"ranger,omitempty"`
}

type opaAuthorizationProviderConfig struct {
	DecisionPath string `json:"decisionPath" yaml:"decisionPath"`
}

type rangerAuthorizationProviderConfig struct {
	AuthenticationMode string            `json:"authenticationMode" yaml:"authenticationMode"`
	ServicePrincipal   string            `json:"servicePrincipal" yaml:"servicePrincipal"`
	AllowInsecureHTTP  bool              `json:"allowInsecureHTTP,omitempty" yaml:"allowInsecureHTTP,omitempty"`
	ServiceType        string            `json:"serviceType" yaml:"serviceType"`
	ServiceName        string            `json:"serviceName" yaml:"serviceName"`
	Resource           string            `json:"resource" yaml:"resource"`
	Permission         string            `json:"permission" yaml:"permission"`
	ContextAttributes  map[string]string `json:"contextAttributes,omitempty" yaml:"contextAttributes,omitempty"`
}

// buildAuthorizationRegistry validates the provider configs and constructs the
// external authorization registry. The configs come from the unified config
// (cfg.Authorization.Providers); source names them in error messages, for
// example "authorization.providers". A nil or empty list yields an empty
// registry. Bearer tokens are read from the environment variable named by
// BearerTokenEnv, so the secret stays in a Secret rather than the config.
func buildAuthorizationRegistry(configs []authorizationProviderConfig, source string) (*authorization.Registry, error) {
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
