package ranger

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAuthorizationRequestJSONContract(t *testing.T) {
	req := AuthorizationRequest{
		RequestID: "req-1",
		User: &UserInfo{
			Name:       "alice",
			Attributes: map[string]any{"tenant": "acme"},
			Groups:     []string{"analysts"},
			Roles:      []string{"reader"},
		},
		Access: &AccessInfo{
			Resource: &ResourceInfo{
				Name:           "semantic-model:retail",
				SubResources:   []string{"metric:revenue"},
				NameMatchScope: MatchSelf,
				Attributes:     map[string]any{"namespace": "sales"},
			},
			Action:      "query",
			Permissions: []string{"select"},
		},
		Context: &AccessContext{
			ServiceType:          "semantic",
			ServiceName:          "production-semantic",
			AccessTime:           1723910400000,
			ClientIPAddress:      "192.0.2.10",
			ForwardedIPAddresses: []string{"198.51.100.4"},
			AdditionalInfo:       map[string]any{"requestVersion": "v1"},
		},
	}

	got, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
		"requestId":"req-1",
		"user":{"name":"alice","attributes":{"tenant":"acme"},"groups":["analysts"],"roles":["reader"]},
		"access":{"resource":{"name":"semantic-model:retail","subResources":["metric:revenue"],"nameMatchScope":"SELF","attributes":{"namespace":"sales"}},"action":"query","permissions":["select"]},
		"context":{"serviceType":"semantic","serviceName":"production-semantic","accessTime":1723910400000,"clientIpAddress":"192.0.2.10","forwardedIpAddresses":["198.51.100.4"],"additionalInfo":{"requestVersion":"v1"}}
	}`
	assertJSONEqual(t, got, []byte(want))
}

func TestAuthorizationResultJSONContract(t *testing.T) {
	fixture := []byte(`{
		"requestId":"req-1",
		"decision":"ALLOW",
		"permissions":{
			"select":{
				"permission":"select",
				"access":{"decision":"ALLOW","policy":{"id":7,"version":3}},
				"dataMask":{"maskType":"MASK","maskedValue":"***","policy":{"id":8,"version":4}},
				"rowFilter":{"filterExpr":"tenant = 'acme'","policy":{"id":9,"version":5}},
				"additionalInfo":{"source":"tag-policy"},
				"subResources":{
					"metric:revenue":{
						"access":{"decision":"DENY","policy":{"id":-1}},
						"additionalInfo":{"reason":"restricted"}
					}
				}
			}
		}
	}`)

	var got AuthorizationResult
	if err := json.Unmarshal(fixture, &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "req-1" || got.Decision != DecisionAllow {
		t.Fatalf("result header = %+v", got)
	}
	selectResult, ok := got.Permissions["select"]
	if !ok || selectResult == nil || selectResult.Access == nil || selectResult.Access.Policy == nil {
		t.Fatalf("select result = %+v", selectResult)
	}
	if selectResult.Access.Decision != DecisionAllow || value(selectResult.Access.Policy.ID) != 7 || value(selectResult.Access.Policy.Version) != 3 {
		t.Fatalf("access result = %+v", selectResult.Access)
	}
	if selectResult.DataMask == nil || selectResult.DataMask.MaskType != "MASK" || selectResult.DataMask.MaskedValue != "***" {
		t.Fatalf("data mask = %+v", selectResult.DataMask)
	}
	if selectResult.RowFilter == nil || selectResult.RowFilter.FilterExpression != "tenant = 'acme'" {
		t.Fatalf("row filter = %+v", selectResult.RowFilter)
	}
	sub := selectResult.SubResources["metric:revenue"]
	if sub == nil || sub.Access == nil || sub.Access.Decision != DecisionDeny || sub.Access.Policy == nil || value(sub.Access.Policy.ID) != -1 {
		t.Fatalf("sub-resource result = %+v", sub)
	}

	roundTrip, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, roundTrip, fixture)
}

func TestMultiAuthorizationAndPermissionsContracts(t *testing.T) {
	permissionsRequest := ResourcePermissionsRequest{
		RequestID: "req-permissions",
		Resource:  &ResourceInfo{Name: "semantic-model:retail", NameMatchScope: MatchSelfOrAnyDescendant},
		Context:   &AccessContext{ServiceType: "semantic", ServiceName: "production-semantic"},
	}
	permissionsRequestJSON, err := json.Marshal(permissionsRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, permissionsRequestJSON, []byte(`{
		"requestId":"req-permissions",
		"resource":{"name":"semantic-model:retail","nameMatchScope":"SELF_OR_ANY_DESCENDANT"},
		"context":{"serviceType":"semantic","serviceName":"production-semantic","accessTime":0}
	}`))

	multi := MultiAuthorizationRequest{
		RequestID: "req-multi",
		User:      &UserInfo{Name: "alice"},
		Accesses: []*AccessInfo{
			{Resource: &ResourceInfo{Name: "metric:revenue"}, Action: "query", Permissions: []string{"select"}},
			{Resource: &ResourceInfo{Name: "dimension:region"}, Action: "query", Permissions: []string{"select"}},
		},
		Context: &AccessContext{ServiceType: "semantic", ServiceName: "production-semantic"},
	}
	blob, err := json.Marshal(multi)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MultiAuthorizationRequest
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, multi) {
		t.Fatalf("multi request round trip = %#v, want %#v", decoded, multi)
	}

	multiResult := MultiAuthorizationResult{
		RequestID: "req-multi",
		Decision:  DecisionPartial,
		Accesses: []*AuthorizationResult{
			{RequestID: "metric:revenue", Decision: DecisionAllow},
			{RequestID: "dimension:region", Decision: DecisionDeny},
		},
	}
	blob, err = json.Marshal(multiResult)
	if err != nil {
		t.Fatal(err)
	}
	var decodedResult MultiAuthorizationResult
	if err := json.Unmarshal(blob, &decodedResult); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedResult, multiResult) {
		t.Fatalf("multi result round trip = %#v, want %#v", decodedResult, multiResult)
	}

	permissions := ResourcePermissions{
		Resource: &ResourceInfo{Name: "semantic-model:retail"},
		Users: PermissionsBySubject{
			"alice": {"select": {Permission: "select", Access: &AccessResult{Decision: DecisionAllow}}},
		},
		Groups: PermissionsBySubject{
			"analysts": {"select": {Permission: "select", Access: &AccessResult{Decision: DecisionAllow}}},
		},
		Roles: PermissionsBySubject{
			"restricted": {"select": {Permission: "select", Access: &AccessResult{Decision: DecisionDeny}}},
		},
	}
	blob, err = json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	var decodedPermissions ResourcePermissions
	if err := json.Unmarshal(blob, &decodedPermissions); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedPermissions, permissions) {
		t.Fatalf("permissions round trip = %#v, want %#v", decodedPermissions, permissions)
	}
}

func TestNullableReferenceEntries(t *testing.T) {
	authorizationJSON := []byte(`{
		"permissions":{
			"missing":null,
			"select":{"permission":"select","subResources":{"metric:revenue":null}}
		}
	}`)
	var result AuthorizationResult
	if err := json.Unmarshal(authorizationJSON, &result); err != nil {
		t.Fatal(err)
	}
	if value, ok := result.Permissions["missing"]; !ok || value != nil {
		t.Fatalf("nullable permission entry = %#v, present=%v", value, ok)
	}
	if value, ok := result.Permissions["select"].SubResources["metric:revenue"]; !ok || value != nil {
		t.Fatalf("nullable sub-resource entry = %#v, present=%v", value, ok)
	}
	roundTrip, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, roundTrip, authorizationJSON)

	permissionsJSON := []byte(`{
		"users":{"alice":{"select":null}},
		"groups":{"analysts":{"select":null}},
		"roles":{"restricted":{"select":null}}
	}`)
	var permissions ResourcePermissions
	if err := json.Unmarshal(permissionsJSON, &permissions); err != nil {
		t.Fatal(err)
	}
	if permissions.Users["alice"]["select"] != nil || permissions.Groups["analysts"]["select"] != nil || permissions.Roles["restricted"]["select"] != nil {
		t.Fatalf("nullable subject permissions were not preserved: %+v", permissions)
	}
	roundTrip, err = json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, roundTrip, permissionsJSON)
}

func TestWireEnums(t *testing.T) {
	for _, decision := range []AccessDecision{DecisionAllow, DecisionDeny, DecisionNotDetermined, DecisionPartial} {
		if !decision.Valid() {
			t.Errorf("decision %q is not valid", decision)
		}
	}
	for _, decision := range []AccessDecision{"", "allow", "UNKNOWN"} {
		if decision.Valid() {
			t.Errorf("decision %q is unexpectedly valid", decision)
		}
	}

	for _, scope := range []ResourceMatchScope{MatchSelf, MatchSelfOrAnyChild, MatchSelfOrAnyDescendant} {
		if !scope.Valid() {
			t.Errorf("scope %q is not valid", scope)
		}
	}
	for _, scope := range []ResourceMatchScope{"", "SELF_OR_CHILD", "self"} {
		if scope.Valid() {
			t.Errorf("scope %q is unexpectedly valid", scope)
		}
	}

	var result AuthorizationResult
	if err := json.Unmarshal([]byte(`{"decision":"FUTURE"}`), &result); err == nil {
		t.Fatal("unknown Ranger decision was decoded")
	}
	if err := json.Unmarshal([]byte(`{"decision":null}`), &result); err != nil || result.Decision != "" {
		t.Fatalf("nullable Ranger decision: result=%+v err=%v", result, err)
	}
	var resource ResourceInfo
	if err := json.Unmarshal([]byte(`{"name":"model:x","nameMatchScope":"CHILD"}`), &resource); err == nil {
		t.Fatal("unknown Ranger resource match scope was decoded")
	}
	if err := json.Unmarshal([]byte(`{"name":"model:x","nameMatchScope":null}`), &resource); err != nil || resource.NameMatchScope != "" {
		t.Fatalf("nullable Ranger scope: resource=%+v err=%v", resource, err)
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v\n%s", err, got)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
