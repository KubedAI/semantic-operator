// Package ranger contains the wire contract for Apache Ranger's dedicated
// remote PDP. The handwritten DTOs track the checked-in Apache Ranger
// 3.0.0-SNAPSHOT source at revision ece62f616c52efe6a0d0e474fb68e84edc8b1b92,
// including RangerPdpREST and the authz-api model classes under sources/ranger.
package ranger

import (
	"encoding/json"
	"fmt"
)

// AccessDecision is Ranger's aggregate or per-permission authorization result.
type AccessDecision string

const (
	DecisionAllow         AccessDecision = "ALLOW"
	DecisionDeny          AccessDecision = "DENY"
	DecisionNotDetermined AccessDecision = "NOT_DETERMINED"
	DecisionPartial       AccessDecision = "PARTIAL"
)

// Valid reports whether the decision is one defined by RangerAuthzResult.
func (d AccessDecision) Valid() bool {
	switch d {
	case DecisionAllow, DecisionDeny, DecisionNotDetermined, DecisionPartial:
		return true
	default:
		return false
	}
}

// UnmarshalJSON rejects decision values outside Ranger's published enum.
func (d *AccessDecision) UnmarshalJSON(data []byte) error {
	var value *string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("access decision from Ranger must be a string or null: %w", err)
	}
	if value == nil {
		*d = ""
		return nil
	}
	decision := AccessDecision(*value)
	if !decision.Valid() {
		return fmt.Errorf("unknown Ranger access decision %q", *value)
	}
	*d = decision
	return nil
}

// ResourceMatchScope controls whether Ranger matches only the named resource
// or also resources below it in the service definition hierarchy.
type ResourceMatchScope string

const (
	MatchSelf                ResourceMatchScope = "SELF"
	MatchSelfOrAnyChild      ResourceMatchScope = "SELF_OR_ANY_CHILD"
	MatchSelfOrAnyDescendant ResourceMatchScope = "SELF_OR_ANY_DESCENDANT"
)

// Valid reports whether the scope is one defined by RangerResourceInfo.
func (s ResourceMatchScope) Valid() bool {
	switch s {
	case MatchSelf, MatchSelfOrAnyChild, MatchSelfOrAnyDescendant:
		return true
	default:
		return false
	}
}

// UnmarshalJSON rejects scope values outside Ranger's published enum.
func (s *ResourceMatchScope) UnmarshalJSON(data []byte) error {
	var value *string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("resource match scope from Ranger must be a string or null: %w", err)
	}
	if value == nil {
		*s = ""
		return nil
	}
	scope := ResourceMatchScope(*value)
	if !scope.Valid() {
		return fmt.Errorf("unknown Ranger resource match scope %q", *value)
	}
	*s = scope
	return nil
}

// AuthorizationRequest is the request accepted by POST /authz/v1/authorize.
type AuthorizationRequest struct {
	RequestID string         `json:"requestId,omitempty"`
	User      *UserInfo      `json:"user,omitempty"`
	Access    *AccessInfo    `json:"access,omitempty"`
	Context   *AccessContext `json:"context,omitempty"`
}

// MultiAuthorizationRequest is accepted by POST /authz/v1/authorizeMulti.
type MultiAuthorizationRequest struct {
	RequestID string         `json:"requestId,omitempty"`
	User      *UserInfo      `json:"user,omitempty"`
	Accesses  []*AccessInfo  `json:"accesses,omitempty"`
	Context   *AccessContext `json:"context,omitempty"`
}

// UserInfo identifies the subject and its Ranger attributes.
type UserInfo struct {
	Name       string         `json:"name,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Groups     []string       `json:"groups,omitempty"`
	Roles      []string       `json:"roles,omitempty"`
}

// AccessInfo describes one resource action and the permissions to evaluate.
type AccessInfo struct {
	Resource    *ResourceInfo `json:"resource,omitempty"`
	Action      string        `json:"action,omitempty"`
	Permissions []string      `json:"permissions,omitempty"`
}

// AccessContext selects the Ranger service and supplies request context.
type AccessContext struct {
	ServiceType          string         `json:"serviceType,omitempty"`
	ServiceName          string         `json:"serviceName,omitempty"`
	AccessTime           int64          `json:"accessTime"`
	ClientIPAddress      string         `json:"clientIpAddress,omitempty"`
	ForwardedIPAddresses []string       `json:"forwardedIpAddresses,omitempty"`
	AdditionalInfo       map[string]any `json:"additionalInfo,omitempty"`
}

// ResourceInfo is Ranger's canonical resource representation.
type ResourceInfo struct {
	Name           string             `json:"name,omitempty"`
	SubResources   []string           `json:"subResources,omitempty"`
	NameMatchScope ResourceMatchScope `json:"nameMatchScope,omitempty"`
	Attributes     map[string]any     `json:"attributes,omitempty"`
}

// AuthorizationResult is returned by POST /authz/v1/authorize.
type AuthorizationResult struct {
	RequestID   string                       `json:"requestId,omitempty"`
	Decision    AccessDecision               `json:"decision,omitempty"`
	Permissions map[string]*PermissionResult `json:"permissions,omitempty"`
}

// MultiAuthorizationResult is returned by POST /authz/v1/authorizeMulti.
type MultiAuthorizationResult struct {
	RequestID string                 `json:"requestId,omitempty"`
	Decision  AccessDecision         `json:"decision,omitempty"`
	Accesses  []*AuthorizationResult `json:"accesses,omitempty"`
}

// PermissionResult holds the access decision and optional Ranger obligations
// for one permission. Callers must not silently ignore obligations they do not
// implement.
type PermissionResult struct {
	Permission     string                 `json:"permission,omitempty"`
	Access         *AccessResult          `json:"access,omitempty"`
	DataMask       *DataMaskResult        `json:"dataMask,omitempty"`
	RowFilter      *RowFilterResult       `json:"rowFilter,omitempty"`
	AdditionalInfo map[string]any         `json:"additionalInfo,omitempty"`
	SubResources   map[string]*ResultInfo `json:"subResources,omitempty"`
}

// ResultInfo holds the result associated with one sub-resource.
type ResultInfo struct {
	Access         *AccessResult    `json:"access,omitempty"`
	DataMask       *DataMaskResult  `json:"dataMask,omitempty"`
	RowFilter      *RowFilterResult `json:"rowFilter,omitempty"`
	AdditionalInfo map[string]any   `json:"additionalInfo,omitempty"`
}

// AccessResult identifies the access decision and matching policy.
type AccessResult struct {
	Decision AccessDecision `json:"decision,omitempty"`
	Policy   *PolicyInfo    `json:"policy,omitempty"`
}

// DataMaskResult describes a Ranger data-masking obligation.
type DataMaskResult struct {
	MaskType    string      `json:"maskType,omitempty"`
	MaskedValue string      `json:"maskedValue,omitempty"`
	Policy      *PolicyInfo `json:"policy,omitempty"`
}

// RowFilterResult describes a Ranger row-filter obligation.
type RowFilterResult struct {
	FilterExpression string      `json:"filterExpr,omitempty"`
	Policy           *PolicyInfo `json:"policy,omitempty"`
}

// PolicyInfo identifies the Ranger policy that produced a result. Pointers
// preserve the distinction between an omitted value and Ranger's meaningful
// sentinel values such as policy ID -1.
type PolicyInfo struct {
	ID      *int64 `json:"id,omitempty"`
	Version *int64 `json:"version,omitempty"`
}

// ResourcePermissionsRequest is accepted by POST /authz/v1/permissions.
type ResourcePermissionsRequest struct {
	RequestID string         `json:"requestId,omitempty"`
	Resource  *ResourceInfo  `json:"resource,omitempty"`
	Context   *AccessContext `json:"context,omitempty"`
}

// PermissionsBySubject maps a user, group, or role name to permission results.
type PermissionsBySubject map[string]map[string]*PermissionResult

// ResourcePermissions is returned by POST /authz/v1/permissions.
type ResourcePermissions struct {
	Resource *ResourceInfo        `json:"resource,omitempty"`
	Users    PermissionsBySubject `json:"users,omitempty"`
	Groups   PermissionsBySubject `json:"groups,omitempty"`
	Roles    PermissionsBySubject `json:"roles,omitempty"`
}
