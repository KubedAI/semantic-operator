// Package provider adapts semantic authorization decisions to the standalone
// Apache Ranger PDP client.
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization/ranger"
)

const (
	maxAttributeCount = 64
	maxAttributeKey   = 128
	maxAttributeValue = 1024
	maxPrincipalSets  = 256
	maxPrincipalValue = 256
	maxResourceName   = 2048
)

var (
	attributeKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)
	placeholderPattern  = regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9]*\}`)
)

// Client is the subset of the Ranger wire client used by query authorization.
type Client interface {
	Authorize(context.Context, ranger.AuthorizationRequest) (ranger.AuthorizationResult, error)
}

// Options binds one server-owned Ranger service and semantic resource mapping.
// ResourceTemplate supports {namespace}, {name}, {resource}, and {version}.
type Options struct {
	ServiceType       string
	ServiceName       string
	ResourceTemplate  string
	Permission        string
	ContextAttributes map[string]string
}

// Provider evaluates semantic query authorization through Ranger service mode.
// The HTTP caller is the semantic server and the request body identifies the
// authenticated end user, so the server principal must be configured as a
// Ranger delegation user for ServiceName.
type Provider struct {
	client             Client
	serviceType        string
	serviceName        string
	resourceTemplate   string
	permission         string
	contextAttributes  map[string]string
	configurationScope string
}

// New constructs a Ranger provider and validates all administrator-controlled
// policy context before the server starts accepting requests.
func New(client Client, opts Options) (*Provider, error) {
	if client == nil {
		return nil, errors.New("Ranger provider client is nil")
	}
	for field, value := range map[string]string{
		"serviceType": opts.ServiceType,
		"serviceName": opts.ServiceName,
		"resource":    opts.ResourceTemplate,
		"permission":  opts.Permission,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("Ranger provider %s is required", field)
		}
		if containsControl(value) {
			return nil, fmt.Errorf("Ranger provider %s contains a control character", field)
		}
	}
	if len(opts.ServiceType) > maxPrincipalValue || len(opts.ServiceName) > maxPrincipalValue || len(opts.Permission) > maxPrincipalValue {
		return nil, fmt.Errorf("Ranger provider serviceType, serviceName, and permission must not exceed %d characters", maxPrincipalValue)
	}
	if len(opts.ResourceTemplate) > maxResourceName {
		return nil, fmt.Errorf("Ranger provider resource must not exceed %d characters", maxResourceName)
	}
	if err := validateResourceTemplate(opts.ResourceTemplate); err != nil {
		return nil, err
	}
	attributes, err := copyAndValidateAttributes(opts.ContextAttributes)
	if err != nil {
		return nil, fmt.Errorf("Ranger provider contextAttributes: %w", err)
	}
	configurationScope := digestJSON(struct {
		ServiceType       string            `json:"serviceType"`
		ServiceName       string            `json:"serviceName"`
		ResourceTemplate  string            `json:"resource"`
		Permission        string            `json:"permission"`
		ContextAttributes map[string]string `json:"contextAttributes,omitempty"`
	}{opts.ServiceType, opts.ServiceName, opts.ResourceTemplate, opts.Permission, attributes})
	return &Provider{
		client: client, serviceType: opts.ServiceType, serviceName: opts.ServiceName,
		resourceTemplate: opts.ResourceTemplate, permission: opts.Permission,
		contextAttributes: attributes, configurationScope: configurationScope,
	}, nil
}

// Decide translates one bounded semantic request and accepts only a complete,
// obligation-free Ranger ALLOW response.
func (p *Provider) Decide(ctx context.Context, input authorization.Input) (authorization.Decision, error) {
	if input.APIVersion != authorization.InputAPIVersion {
		return authorization.Decision{}, fmt.Errorf("unsupported authorization input API version %q", input.APIVersion)
	}
	if err := validateIdentity(input.Identity); err != nil {
		return authorization.Decision{}, err
	}
	if input.Environment.AccessTimeUnixMilli <= 0 {
		return authorization.Decision{}, errors.New("authorization access time must be positive")
	}
	if strings.TrimSpace(input.Environment.Adapter) == "" || containsControl(input.Environment.Adapter) {
		return authorization.Decision{}, errors.New("authorization adapter must be non-empty and contain no control characters")
	}
	resourceName := expandResource(p.resourceTemplate, input)
	if resourceName == "" || len(resourceName) > maxResourceName || containsControl(resourceName) {
		return authorization.Decision{}, errors.New("expanded Ranger resource is empty, oversized, or contains a control character")
	}
	requestData, err := json.Marshal(input.Request)
	if err != nil {
		return authorization.Decision{}, fmt.Errorf("encoding Ranger semantic request context: %w", err)
	}
	additionalInfo := make(map[string]any, len(p.contextAttributes)+7)
	for name, value := range p.contextAttributes {
		additionalInfo[name] = value
	}
	additionalInfo["clientType"] = input.Environment.Adapter
	additionalInfo["requestData"] = string(requestData)
	additionalInfo["semantic.apiVersion"] = input.APIVersion
	additionalInfo["semantic.model.name"] = input.Model.Name
	additionalInfo["semantic.model.namespace"] = input.Model.Namespace
	additionalInfo["semantic.model.resource"] = input.Model.Resource
	additionalInfo["semantic.model.version"] = input.Model.Version

	attributes := make(map[string]any, len(input.Identity.Claims))
	for name, value := range input.Identity.Claims {
		attributes[name] = value
	}
	requestID := requestDigest(input)
	result, err := p.client.Authorize(ctx, ranger.AuthorizationRequest{
		RequestID: requestID,
		User: &ranger.UserInfo{
			Name: input.Identity.Principal, Groups: input.Identity.Groups,
			Roles: input.Identity.Roles, Attributes: attributes,
		},
		Access: &ranger.AccessInfo{
			Resource: &ranger.ResourceInfo{Name: resourceName, NameMatchScope: ranger.MatchSelf},
			Action:   string(input.Action), Permissions: []string{p.permission},
		},
		Context: &ranger.AccessContext{
			ServiceType: p.serviceType, ServiceName: p.serviceName,
			AccessTime: input.Environment.AccessTimeUnixMilli, AdditionalInfo: additionalInfo,
		},
	})
	if err != nil {
		return authorization.Decision{}, err
	}
	return p.validateResult(requestID, result)
}

func (p *Provider) validateResult(requestID string, result ranger.AuthorizationResult) (authorization.Decision, error) {
	if result.RequestID != requestID {
		return authorization.Decision{}, fmt.Errorf("Ranger response requestId %q does not match request", result.RequestID)
	}
	if !result.Decision.Valid() {
		return authorization.Decision{}, errors.New("Ranger response is missing a valid aggregate decision")
	}
	if len(result.Permissions) != 1 {
		return authorization.Decision{}, fmt.Errorf("Ranger response contains %d permissions, expected exactly one", len(result.Permissions))
	}
	permission, ok := result.Permissions[p.permission]
	if !ok || permission == nil {
		return authorization.Decision{}, fmt.Errorf("Ranger response is missing permission %q", p.permission)
	}
	if permission.Permission != p.permission {
		return authorization.Decision{}, fmt.Errorf("Ranger permission result names %q, expected %q", permission.Permission, p.permission)
	}
	if permission.Access == nil || !permission.Access.Decision.Valid() {
		return authorization.Decision{}, fmt.Errorf("Ranger permission %q is missing a valid access decision", p.permission)
	}
	if permission.Access.Decision != result.Decision {
		return authorization.Decision{}, fmt.Errorf("Ranger aggregate decision %q disagrees with permission decision %q", result.Decision, permission.Access.Decision)
	}
	if permission.DataMask != nil || permission.RowFilter != nil || len(permission.AdditionalInfo) != 0 || len(permission.SubResources) != 0 {
		return authorization.Decision{}, fmt.Errorf("Ranger permission %q returned unsupported obligations", p.permission)
	}
	if result.Decision != ranger.DecisionAllow {
		return authorization.Decision{Allow: false}, nil
	}
	revision := digestJSON(struct {
		Configuration string             `json:"configuration"`
		Policy        *ranger.PolicyInfo `json:"policy,omitempty"`
	}{p.configurationScope, permission.Access.Policy})
	return authorization.Decision{Allow: true, Revision: revision}, nil
}

func validateIdentity(identity governance.Identity) error {
	if strings.TrimSpace(identity.Principal) == "" {
		return errors.New("Ranger service-mode authorization requires a non-empty principal")
	}
	if len(identity.Principal) > maxPrincipalValue || containsControl(identity.Principal) {
		return fmt.Errorf("authorization principal must not exceed %d characters or contain controls", maxPrincipalValue)
	}
	if len(identity.Groups) > maxPrincipalSets || len(identity.Roles) > maxPrincipalSets {
		return fmt.Errorf("authorization groups and roles must each contain at most %d values", maxPrincipalSets)
	}
	for kind, values := range map[string][]string{"group": identity.Groups, "role": identity.Roles} {
		for _, value := range values {
			if value == "" || len(value) > maxPrincipalValue || containsControl(value) {
				return fmt.Errorf("authorization %s values must be non-empty, at most %d characters, and contain no controls", kind, maxPrincipalValue)
			}
		}
	}
	if _, err := copyAndValidateAttributes(identity.Claims); err != nil {
		return fmt.Errorf("authorization subject attributes: %w", err)
	}
	return nil
}

func copyAndValidateAttributes(input map[string]string) (map[string]string, error) {
	if len(input) > maxAttributeCount {
		return nil, fmt.Errorf("must contain at most %d entries", maxAttributeCount)
	}
	out := make(map[string]string, len(input))
	for name, value := range input {
		if len(name) == 0 || len(name) > maxAttributeKey || !attributeKeyPattern.MatchString(name) {
			return nil, fmt.Errorf("attribute name %q must match %s and contain at most %d characters", name, attributeKeyPattern, maxAttributeKey)
		}
		if strings.HasPrefix(name, "semantic.") || name == "clientType" || name == "requestData" {
			return nil, fmt.Errorf("attribute name %q is managed by the Ranger provider", name)
		}
		if len(value) > maxAttributeValue || containsControl(value) {
			return nil, fmt.Errorf("attribute %q must not exceed %d characters or contain controls", name, maxAttributeValue)
		}
		out[name] = value
	}
	return out, nil
}

func validateResourceTemplate(template string) error {
	allowed := map[string]bool{"{namespace}": true, "{name}": true, "{resource}": true, "{version}": true}
	matches := placeholderPattern.FindAllString(template, -1)
	if len(matches) == 0 {
		return errors.New("Ranger provider resource must contain at least one model placeholder")
	}
	for _, match := range matches {
		if !allowed[match] {
			return fmt.Errorf("Ranger provider resource contains unsupported placeholder %q", match)
		}
	}
	without := placeholderPattern.ReplaceAllString(template, "")
	if strings.ContainsAny(without, "{}") {
		return errors.New("Ranger provider resource contains malformed placeholder syntax")
	}
	return nil
}

func expandResource(template string, input authorization.Input) string {
	return strings.NewReplacer(
		"{namespace}", input.Model.Namespace,
		"{name}", input.Model.Name,
		"{resource}", input.Model.Resource,
		"{version}", input.Model.Version,
	).Replace(template)
}

func requestDigest(input authorization.Input) string {
	return digestJSON(input)[:32]
}

func digestJSON(value any) string {
	blob, _ := json.Marshal(value)
	digest := sha256.Sum256(blob)
	return hex.EncodeToString(digest[:])
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

// StableContextKeys returns configured static context keys in deterministic
// order. It is used only for diagnostics and tests, never to expose values.
func (p *Provider) StableContextKeys() []string {
	keys := make([]string, 0, len(p.contextAttributes))
	for key := range p.contextAttributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
