package datahub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// gqlServer serves canned GraphQL responses keyed by the requested URN. It
// answers either query shape the client can send, like a real server: the
// batched `entities(urns:)` form and the single `dataset(urn:)` form. Set
// rejectBatch to emulate an older schema that does not know `entities`.
type gqlServer struct {
	*httptest.Server
	byURN       map[string]string
	urns        []string // URNs asked for, across both query shapes
	calls       int      // HTTP requests received
	headers     http.Header
	status      int
	rejectBatch bool
}

func newGQLServer(t *testing.T, byURN map[string]string) *gqlServer {
	t.Helper()
	s := &gqlServer{byURN: byURN}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.headers = r.Header.Clone()
		s.calls++
		if s.status != 0 {
			w.WriteHeader(s.status)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string `json:"query"`
			Variables struct {
				URN  string   `json:"urn"`
				URNs []string `json:"urns"`
			} `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(req.Query, "entities(") {
			if s.rejectBatch {
				_, _ = io.WriteString(w, `{"errors":[{"message":"ValidationError: Cannot query field 'entities' on type 'Query'"}]}`)
				return
			}
			s.urns = append(s.urns, req.Variables.URNs...)
			entities := make([]string, 0, len(req.Variables.URNs))
			for _, u := range req.Variables.URNs {
				canned, ok := s.byURN[u]
				if !ok {
					entities = append(entities, "null") // unknown URN
					continue
				}
				// Reshape the canned single-dataset body into a batch entity
				// by lifting data.dataset and attaching its urn.
				var single struct {
					Data struct {
						Dataset json.RawMessage `json:"dataset"`
					} `json:"data"`
				}
				_ = json.Unmarshal([]byte(canned), &single)
				urnJSON, _ := json.Marshal(u)
				entities = append(entities, `{"urn":`+string(urnJSON)+`,`+strings.TrimPrefix(string(single.Data.Dataset), "{"))
			}
			_, _ = io.WriteString(w, `{"data":{"entities":[`+strings.Join(entities, ",")+`]}}`)
			return
		}

		s.urns = append(s.urns, req.Variables.URN)
		if resp, ok := s.byURN[req.Variables.URN]; ok {
			_, _ = io.WriteString(w, resp)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"dataset":null}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

const customerResponse = `{"data":{"dataset":{
  "properties": {"description": "One row per customer"},
  "deprecation": {"deprecated": false},
  "glossaryTerms": {"terms": [{"term": {"properties": {"name": "Buyer"}}}]},
  "schemaMetadata": {"fields": [
    {"fieldPath": "c_customer_sk", "description": "Surrogate key"},
    {"fieldPath": "[version=2.0].[type=struct].[type=string].c_email_address",
     "description": "Contact email",
     "globalTags": {"tags": [{"tag": {"properties": {"name": "PII"}}}]}},
    {"fieldPath": "address.line1", "description": "nested, no flat column"}
  ]}
}}}`

const deprecatedResponse = `{"data":{"dataset":{
  "properties": {"description": "Legacy feature table"},
  "deprecation": {"deprecated": true},
  "schemaMetadata": {"fields": [
    {"fieldPath": "f_id",
     "glossaryTerms": {"terms": [{"term": {"properties": {"name": "Confidential"}}}]}}
  ]}
}}}`

func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(Options{URL: url, Platform: "iceberg"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDescribeTablesMapsMetadata(t *testing.T) {
	urn := "urn:li:dataset:(urn:li:dataPlatform:iceberg,osi_demo.customer,PROD)"
	s := newGQLServer(t, map[string]string{urn: customerResponse})
	c := newTestClient(t, s.URL)

	meta, err := c.DescribeTables(context.Background(), "osi_demo", []string{"customer"})
	if err != nil {
		t.Fatal(err)
	}
	tm, ok := meta["customer"]
	if !ok {
		t.Fatalf("no metadata for customer: %+v", meta)
	}
	if tm.Description != "One row per customer" {
		t.Errorf("description = %q", tm.Description)
	}
	if len(tm.Synonyms) != 1 || tm.Synonyms[0] != "Buyer" {
		t.Errorf("glossary terms should become synonyms, got %v", tm.Synonyms)
	}
	// v2 fieldPath encoding must be decoded to the plain column name.
	email, ok := tm.Fields["c_email_address"]
	if !ok {
		t.Fatalf("v2 fieldPath not decoded; fields: %v", keys(tm.Fields))
	}
	if !email.Sensitive {
		t.Error("PII tag must mark the column sensitive")
	}
	if email.Description != "Contact email" {
		t.Errorf("field description = %q", email.Description)
	}
	if sk := tm.Fields["c_customer_sk"]; sk.Sensitive {
		t.Error("untagged column must not be sensitive")
	}
	// Nested struct fields have no flat column, so they are skipped.
	if _, ok := tm.Fields["line1"]; ok {
		t.Error("nested field should not produce a column entry")
	}
}

func TestSensitiveViaGlossaryTermAndDeprecation(t *testing.T) {
	urn := "urn:li:dataset:(urn:li:dataPlatform:iceberg,osi_demo.features,PROD)"
	s := newGQLServer(t, map[string]string{urn: deprecatedResponse})
	c := newTestClient(t, s.URL)

	meta, err := c.DescribeTables(context.Background(), "osi_demo", []string{"features"})
	if err != nil {
		t.Fatal(err)
	}
	tm := meta["features"]
	if !tm.Deprecated {
		t.Error("deprecation must be reported")
	}
	if !tm.Fields["f_id"].Sensitive {
		t.Error("a Confidential glossary term must mark the column sensitive")
	}
}

func TestUnknownDatasetIsOmittedNotAnError(t *testing.T) {
	s := newGQLServer(t, nil) // every URN resolves to null
	c := newTestClient(t, s.URL)

	meta, err := c.DescribeTables(context.Background(), "osi_demo", []string{"customer", "store_sales"})
	if err != nil {
		t.Fatalf("uncatalogued tables must not error: %v", err)
	}
	if len(meta) != 0 {
		t.Errorf("expected no metadata, got %v", keys(meta))
	}
	if len(s.urns) != 2 {
		t.Errorf("expected one query per table, got %v", s.urns)
	}
}

func TestManyTablesUseOneRoundTrip(t *testing.T) {
	// A real database has hundreds of tables; derivation must not make one
	// HTTP call per table.
	urn := func(t string) string {
		return "urn:li:dataset:(urn:li:dataPlatform:iceberg,osi_demo." + t + ",PROD)"
	}
	s := newGQLServer(t, map[string]string{
		urn("customer"): customerResponse,
		urn("features"): deprecatedResponse,
	})
	c := newTestClient(t, s.URL)

	meta, err := c.DescribeTables(context.Background(), "osi_demo",
		[]string{"customer", "features", "store_sales", "item", "store"})
	if err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 {
		t.Errorf("expected 1 batched call for 5 tables, got %d", s.calls)
	}
	// Known tables are mapped back to the right names by URN; unknown ones
	// are simply absent.
	if len(meta) != 2 {
		t.Fatalf("meta = %v, want customer + features", keys(meta))
	}
	if meta["customer"].Description != "One row per customer" {
		t.Errorf("customer metadata mismatched: %+v", meta["customer"])
	}
	if !meta["features"].Deprecated {
		t.Errorf("features metadata mismatched: %+v", meta["features"])
	}
}

func TestStubEntityIsNotCountedAsEnriched(t *testing.T) {
	// Real DataHub answers an uncatalogued URN with a stub entity (every
	// field null) instead of null, so it must not be reported as enriched.
	s := newGQLServer(t, map[string]string{
		"urn:li:dataset:(urn:li:dataPlatform:iceberg,osi_demo.ghost,PROD)": `{"data":{"dataset":{
		  "properties": null, "deprecation": null, "globalTags": null,
		  "glossaryTerms": null, "schemaMetadata": null}}}`,
	})
	c := newTestClient(t, s.URL)

	meta, err := c.DescribeTables(context.Background(), "osi_demo", []string{"ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 0 {
		t.Errorf("stub entity must not count as enriched, got %v", keys(meta))
	}
}

func TestFallsBackWhenBatchQueryUnsupported(t *testing.T) {
	// An older GraphQL schema rejects `entities`; the client must retry with
	// per-dataset queries rather than silently returning nothing.
	urn := "urn:li:dataset:(urn:li:dataPlatform:iceberg,osi_demo.customer,PROD)"
	s := newGQLServer(t, map[string]string{urn: customerResponse})
	s.rejectBatch = true
	c := newTestClient(t, s.URL)

	meta, err := c.DescribeTables(context.Background(), "osi_demo", []string{"customer", "features"})
	if err != nil {
		t.Fatalf("fallback path must succeed: %v", err)
	}
	if _, ok := meta["customer"]; !ok {
		t.Fatalf("fallback lost metadata: %v", keys(meta))
	}
	// One rejected batch, then one call per table.
	if s.calls != 3 {
		t.Errorf("calls = %d, want 1 rejected batch + 2 singles", s.calls)
	}
}

func TestGraphQLFailureIsReported(t *testing.T) {
	s := newGQLServer(t, nil)
	s.status = http.StatusInternalServerError
	c := newTestClient(t, s.URL)

	if _, err := c.DescribeTables(context.Background(), "osi_demo", []string{"customer"}); err == nil {
		t.Fatal("a server failure must be reported to the caller")
	}
}

func TestTokenIsSentAsBearer(t *testing.T) {
	s := newGQLServer(t, nil)
	c, err := New(Options{URL: s.URL, Platform: "iceberg", Token: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DescribeTables(context.Background(), "osi_demo", []string{"customer"}); err != nil {
		t.Fatal(err)
	}
	if got := s.headers.Get("Authorization"); got != "Bearer secret-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestURNRespectsPlatformAndEnv(t *testing.T) {
	s := newGQLServer(t, nil)
	c, err := New(Options{URL: s.URL, Platform: "trino", Env: "DEV"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DescribeTables(context.Background(), "analytics", []string{"orders"}); err != nil {
		t.Fatal(err)
	}
	want := "urn:li:dataset:(urn:li:dataPlatform:trino,analytics.orders,DEV)"
	if len(s.urns) != 1 || s.urns[0] != want {
		t.Errorf("urn = %v, want %s", s.urns, want)
	}
}

func TestNewValidatesRequiredOptions(t *testing.T) {
	if _, err := New(Options{Platform: "iceberg"}); err == nil {
		t.Error("missing URL must error")
	}
	if _, err := New(Options{URL: "http://x"}); err == nil {
		t.Error("missing platform must error")
	}
}

func TestCustomSensitiveTags(t *testing.T) {
	urn := "urn:li:dataset:(urn:li:dataPlatform:iceberg,osi_demo.customer,PROD)"
	s := newGQLServer(t, map[string]string{urn: customerResponse})
	// "PII" is not in this list, so the column must NOT be marked sensitive.
	c, err := New(Options{URL: s.URL, Platform: "iceberg", SensitiveTags: []string{"HighlySecret"}})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := c.DescribeTables(context.Background(), "osi_demo", []string{"customer"})
	if err != nil {
		t.Fatal(err)
	}
	if meta["customer"].Fields["c_email_address"].Sensitive {
		t.Error("only the configured tags should mark a column sensitive")
	}
}

func TestColumnNameDecoding(t *testing.T) {
	cases := map[string]string{
		"c_email_address": "c_email_address",
		"[version=2.0].[type=struct].[type=string].c_email_address": "c_email_address",
		"address.line1": "",
		"[version=2.0].[type=struct].[type=struct].address.line1": "",
	}
	for in, want := range cases {
		if got := columnName(in); got != want {
			t.Errorf("columnName(%q) = %q, want %q", in, got, want)
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
