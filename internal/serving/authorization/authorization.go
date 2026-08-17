// Package authorization coordinates query-time external policy decisions.
package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

// ErrUnavailable marks a provider configuration, transport, or response
// failure. Adapters map it to 503, distinct from an explicit policy denial.
var ErrUnavailable = errors.New("external authorization unavailable")

// Action is the operation presented to a provider.
type Action string

const (
	// InputAPIVersion versions the external policy contract independently of
	// the SemanticModel CRD version.
	InputAPIVersion = "authorization.semantic.ossie.io/v1alpha1"

	// ActionQuery covers planning and execution.
	ActionQuery Action = "query"
)

// Environment contains trusted dynamic request context supplied by the
// semantic server, never by a SemanticModel or policy provider.
type Environment struct {
	AccessTimeUnixMilli int64  `json:"accessTimeUnixMilli"`
	Adapter             string `json:"adapter,omitempty"`
}

// Model identifies the exact published artifact being authorized.
type Model struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Namespace string `json:"namespace,omitempty"`
	Resource  string `json:"resource,omitempty"`
}

// Input is the bounded semantic context sent to a provider. It intentionally
// contains no compiled SQL or raw metric expressions.
type Input struct {
	APIVersion  string              `json:"apiVersion"`
	Action      Action              `json:"action"`
	Identity    governance.Identity `json:"identity"`
	Model       Model               `json:"model"`
	Request     planner.Request     `json:"request"`
	Environment Environment         `json:"environment"`
}

// NewQueryInput constructs deterministic identity and request fields. Group
// and role order is not semantically meaningful, so sorting prevents token
// claim order from changing policy input and cache behavior. Access time is
// supplied explicitly by the serving layer.
func NewQueryInput(m *planner.CompiledModel, req planner.Request, id governance.Identity, environment Environment) Input {
	id.Groups = append([]string(nil), id.Groups...)
	id.Roles = append([]string(nil), id.Roles...)
	if len(id.Groups) == 0 && len(id.Roles) == 0 && m.Governance != nil && m.Governance.DefaultRole != "" {
		id.Roles = []string{m.Governance.DefaultRole}
	}
	sort.Strings(id.Groups)
	sort.Strings(id.Roles)
	return Input{
		APIVersion:  InputAPIVersion,
		Action:      ActionQuery,
		Identity:    id,
		Environment: environment,
		Model: Model{
			Name: m.Name, Version: m.Version,
			Namespace: m.Namespace, Resource: m.Resource,
		},
		Request: req,
	}
}

// Decision is the provider's validated allow/deny result. Revision must be a
// stable policy revision, not a per-request decision ID.
type Decision struct {
	Allow    bool   `json:"allow"`
	Revision string `json:"revision,omitempty"`
}

// Provider evaluates one external authorization decision. Implementations
// bind provider-specific paths and service names during construction, bound
// all network I/O and response data, and return only validated decisions.
type Provider interface {
	Decide(ctx context.Context, input Input) (Decision, error)
}

// Authorizer is the Service dependency used to resolve a logical provider.
type Authorizer interface {
	Authorize(ctx context.Context, providerRef string, input Input) (Decision, error)
}

// Registry maps model-owned logical references to administrator-configured
// providers. Connection details never come from a SemanticModel.

var providerNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateProviderName requires the same lowercase DNS-label shape accepted by
// SemanticModel providerRef. A configured provider that no valid model can
// reference is a startup error, not a delayed query failure.
func ValidateProviderName(name string) error {
	if len(name) > 63 || !providerNamePattern.MatchString(name) {
		return fmt.Errorf("authorization provider name %q must be a lowercase DNS label and less than 64 characters", name)
	}
	return nil
}

type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds one logical provider name. Duplicate registration is an error
// so startup configuration cannot silently replace an authorization boundary.
func (r *Registry) Register(name string, provider Provider) error {
	if r == nil {
		return errors.New("authorization registry is nil")
	}
	if err := ValidateProviderName(name); err != nil {
		return err
	}
	if provider == nil {
		return fmt.Errorf("authorization provider %q is nil", name)
	}
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("authorization provider %q is configured more than once", name)
	}
	r.providers[name] = provider
	return nil
}

// Authorize evaluates one provider and converts an explicit deny into the
// same ErrUnauthorized marker used by built-in governance.
func (r *Registry) Authorize(ctx context.Context, providerRef string, input Input) (Decision, error) {
	if r == nil {
		return Decision{}, fmt.Errorf("%w: provider %q is not configured", ErrUnavailable, providerRef)
	}
	provider, ok := r.providers[providerRef]
	if !ok {
		return Decision{}, fmt.Errorf("%w: provider %q is not configured", ErrUnavailable, providerRef)
	}
	decision, err := provider.Decide(ctx, input)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return Decision{}, err
		}
		return Decision{}, fmt.Errorf("%w: provider %q: %v", ErrUnavailable, providerRef, err)
	}
	if !decision.Allow {
		return Decision{}, fmt.Errorf("%w: external provider %q denied action %q", governance.ErrUnauthorized, providerRef, input.Action)
	}
	return decision, nil
}

// Fingerprint identifies the authorization scope used for cache isolation.
// It includes only stable, validated values.
func Fingerprint(providerRef string, identity governance.Identity, decision Decision) string {
	b, _ := json.Marshal(struct {
		Provider       string   `json:"provider"`
		IdentityDigest string   `json:"identityDigest"`
		Decision       Decision `json:"decision"`
	}{
		Provider:       providerRef,
		IdentityDigest: governance.IdentityDigest(identity),
		Decision:       decision,
	})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
