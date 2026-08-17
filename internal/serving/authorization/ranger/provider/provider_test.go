package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization/ranger"
)

type stubClient struct {
	request ranger.AuthorizationRequest
	result  ranger.AuthorizationResult
	err     error
}

func (s *stubClient) Authorize(_ context.Context, request ranger.AuthorizationRequest) (ranger.AuthorizationResult, error) {
	s.request = request
	result := s.result
	if result.RequestID == "echo" {
		result.RequestID = request.RequestID
	}
	return result, s.err
}

func allowResult(permission string) ranger.AuthorizationResult {
	id, version := int64(17), int64(4)
	return ranger.AuthorizationResult{
		RequestID: "echo", Decision: ranger.DecisionAllow,
		Permissions: map[string]*ranger.PermissionResult{
			permission: {
				Permission: permission,
				Access: &ranger.AccessResult{
					Decision: ranger.DecisionAllow,
					Policy:   &ranger.PolicyInfo{ID: &id, Version: &version},
				},
			},
		},
	}
}

func validInput() authorization.Input {
	return authorization.Input{
		APIVersion: authorization.InputAPIVersion,
		Action:     authorization.ActionQuery,
		Identity: governance.Identity{
			Principal: "alice", Groups: []string{"analysts"}, Roles: []string{"reader"},
			Claims: map[string]string{"department": "finance", "tenant": "acme"},
		},
		Model: authorization.Model{
			Name: "retail", Namespace: "analytics", Resource: "retail-model", Version: "v7",
		},
		Request: planner.Request{
			Model: "retail", Metrics: []string{"revenue"}, Dimensions: []string{"sales.region"},
			Filters: []planner.Filter{{Field: "sales.region", Op: "=", Value: "west"}}, Limit: 10,
		},
		Environment: authorization.Environment{AccessTimeUnixMilli: 1_700_000_000_123, Adapter: "rest"},
	}
}

func newProvider(t *testing.T, client Client, attributes map[string]string) *Provider {
	t.Helper()
	provider, err := New(client, Options{
		ServiceType: "semantic-operator", ServiceName: "semantic-prod",
		ResourceTemplate: "semantic-model:namespace={namespace},model={resource}",
		Permission:       "query", ContextAttributes: attributes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestDecideTranslatesCompleteServiceModeRequest(t *testing.T) {
	client := &stubClient{result: allowResult("query")}
	static := map[string]string{"environment": "production", "clusterName": "analytics-prod"}
	provider := newProvider(t, client, static)
	static["environment"] = "tampered"

	decision, err := provider.Decide(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || len(decision.Revision) != 64 {
		t.Fatalf("decision = %+v", decision)
	}
	request := client.request
	if request.RequestID == "" || request.User == nil || request.User.Name != "alice" {
		t.Fatalf("request identity = %+v", request)
	}
	if !reflect.DeepEqual(request.User.Groups, []string{"analysts"}) || !reflect.DeepEqual(request.User.Roles, []string{"reader"}) {
		t.Fatalf("groups/roles were not preserved: %+v", request.User)
	}
	if request.User.Attributes["department"] != "finance" || request.User.Attributes["tenant"] != "acme" {
		t.Fatalf("subject attributes = %+v", request.User.Attributes)
	}
	if request.Access == nil || request.Access.Resource == nil || request.Access.Resource.Name != "semantic-model:namespace=analytics,model=retail-model" {
		t.Fatalf("resource mapping = %+v", request.Access)
	}
	if request.Access.Action != "query" || !reflect.DeepEqual(request.Access.Permissions, []string{"query"}) {
		t.Fatalf("access mapping = %+v", request.Access)
	}
	if request.Context == nil || request.Context.ServiceType != "semantic-operator" || request.Context.ServiceName != "semantic-prod" || request.Context.AccessTime != 1_700_000_000_123 {
		t.Fatalf("Ranger context = %+v", request.Context)
	}
	info := request.Context.AdditionalInfo
	if info["environment"] != "production" || info["clusterName"] != "analytics-prod" || info["clientType"] != "rest" {
		t.Fatalf("static/dynamic context = %+v", info)
	}
	if info["semantic.model.name"] != "retail" || info["semantic.model.namespace"] != "analytics" ||
		info["semantic.model.resource"] != "retail-model" || info["semantic.model.version"] != "v7" {
		t.Fatalf("model context = %+v", info)
	}
	requestData, ok := info["requestData"].(string)
	if !ok || !strings.Contains(requestData, `"metrics":["revenue"]`) || !strings.Contains(requestData, `"filters"`) {
		t.Fatalf("semantic request context = %#v", info["requestData"])
	}
	if got := provider.StableContextKeys(); !reflect.DeepEqual(got, []string{"clusterName", "environment"}) {
		t.Fatalf("context keys = %v", got)
	}
}

func TestDecideMapsNonAllowDecisionsToDeny(t *testing.T) {
	for _, decision := range []ranger.AccessDecision{ranger.DecisionDeny, ranger.DecisionNotDetermined, ranger.DecisionPartial} {
		t.Run(string(decision), func(t *testing.T) {
			result := allowResult("query")
			result.Decision = decision
			result.Permissions["query"].Access.Decision = decision
			provider := newProvider(t, &stubClient{result: result}, nil)
			got, err := provider.Decide(context.Background(), validInput())
			if err != nil || got.Allow {
				t.Fatalf("decision=%+v err=%v", got, err)
			}
		})
	}
}

func TestDecideRejectsMalformedResponsesAndObligations(t *testing.T) {
	cases := map[string]func(*ranger.AuthorizationResult){
		"request ID":       func(r *ranger.AuthorizationResult) { r.RequestID = "wrong" },
		"missing decision": func(r *ranger.AuthorizationResult) { r.Decision = "" },
		"extra permission": func(r *ranger.AuthorizationResult) { r.Permissions["select"] = r.Permissions["query"] },
		"missing access":   func(r *ranger.AuthorizationResult) { r.Permissions["query"].Access = nil },
		"disagreement":     func(r *ranger.AuthorizationResult) { r.Permissions["query"].Access.Decision = ranger.DecisionDeny },
		"mask": func(r *ranger.AuthorizationResult) {
			r.Permissions["query"].DataMask = &ranger.DataMaskResult{MaskType: "MASK"}
		},
		"row filter": func(r *ranger.AuthorizationResult) {
			r.Permissions["query"].RowFilter = &ranger.RowFilterResult{FilterExpression: "tenant = 'acme'"}
		},
		"additional": func(r *ranger.AuthorizationResult) {
			r.Permissions["query"].AdditionalInfo = map[string]any{"duty": "audit"}
		},
		"subresource": func(r *ranger.AuthorizationResult) {
			r.Permissions["query"].SubResources = map[string]*ranger.ResultInfo{"metric:revenue": {}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			result := allowResult("query")
			mutate(&result)
			provider := newProvider(t, &stubClient{result: result}, nil)
			if _, err := provider.Decide(context.Background(), validInput()); err == nil {
				t.Fatal("malformed or obligated response was accepted")
			}
		})
	}
}

func TestDecideRejectsInvalidInputAndPropagatesClientFailure(t *testing.T) {
	provider := newProvider(t, &stubClient{result: allowResult("query")}, nil)
	cases := map[string]func(*authorization.Input){
		"version":     func(i *authorization.Input) { i.APIVersion = "v0" },
		"principal":   func(i *authorization.Input) { i.Identity.Principal = "" },
		"access time": func(i *authorization.Input) { i.Environment.AccessTimeUnixMilli = 0 },
		"adapter":     func(i *authorization.Input) { i.Environment.Adapter = "" },
		"claim key":   func(i *authorization.Input) { i.Identity.Claims = map[string]string{"bad key": "x"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := validInput()
			mutate(&input)
			if _, err := provider.Decide(context.Background(), input); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
	failure := errors.New("PDP unavailable")
	provider = newProvider(t, &stubClient{err: failure}, nil)
	if _, err := provider.Decide(context.Background(), validInput()); !errors.Is(err, failure) {
		t.Fatalf("client failure = %v", err)
	}
}

func TestNewValidatesProviderConfiguration(t *testing.T) {
	valid := Options{
		ServiceType: "semantic-operator", ServiceName: "semantic-prod",
		ResourceTemplate: "semantic-model:{namespace}/{resource}", Permission: "query",
	}
	cases := map[string]func(*Options){
		"service type":    func(o *Options) { o.ServiceType = "" },
		"service name":    func(o *Options) { o.ServiceName = "" },
		"resource":        func(o *Options) { o.ResourceTemplate = "" },
		"permission":      func(o *Options) { o.Permission = "" },
		"no placeholder":  func(o *Options) { o.ResourceTemplate = "semantic-model:retail" },
		"bad placeholder": func(o *Options) { o.ResourceTemplate = "semantic-model:{dataset}" },
		"managed context": func(o *Options) { o.ContextAttributes = map[string]string{"requestData": "forged"} },
		"bad context key": func(o *Options) { o.ContextAttributes = map[string]string{"bad key": "x"} },
		"control value":   func(o *Options) { o.ContextAttributes = map[string]string{"environment": "prod\nforged"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if _, err := New(&stubClient{}, opts); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
	if _, err := New(nil, valid); err == nil {
		t.Fatal("nil client was accepted")
	}
}

func TestRevisionScopesStaticContextAndPolicy(t *testing.T) {
	client := &stubClient{result: allowResult("query")}
	a := newProvider(t, client, map[string]string{"environment": "prod"})
	decisionA, err := a.Decide(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	b := newProvider(t, &stubClient{result: allowResult("query")}, map[string]string{"environment": "stage"})
	decisionB, err := b.Decide(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if decisionA.Revision == decisionB.Revision {
		t.Fatal("static context did not scope the policy revision")
	}
	changed := allowResult("query")
	version := int64(5)
	changed.Permissions["query"].Access.Policy.Version = &version
	c := newProvider(t, &stubClient{result: changed}, map[string]string{"environment": "prod"})
	decisionC, err := c.Decide(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if decisionA.Revision == decisionC.Revision {
		t.Fatal("Ranger policy version did not scope the revision")
	}
}
