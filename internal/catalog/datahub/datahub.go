// Package datahub implements catalog.Enricher against a DataHub instance.
//
// DataHub holds the business meaning a physical catalog does not: curated
// descriptions, glossary terms, ownership, and classification tags. It serves
// an ingested copy of the schema, so it decorates a scaffold derived from the
// live engine rather than defining one. Everything here is best effort: an
// unknown dataset or column simply yields no metadata.
package datahub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/catalog"
)

// Options configures the client. Token is optional: DataHub deployments
// without metadata-service auth accept unauthenticated GraphQL.
type Options struct {
	// URL is the GMS base URL, e.g. http://datahub-gms.datahub.svc:8080.
	URL string
	// Token authenticates to GMS. A personal access token is sent as
	// "Bearer <token>". A value that already names a scheme (for example
	// "Basic __datahub_system:<secret>", which is how DataHub's system
	// credential authenticates) is sent verbatim.
	Token string
	// Platform is the DataHub data platform the datasets belong to
	// (e.g. "iceberg", "trino", "hive"). It scopes URN construction.
	Platform string
	// DatasetPrefix is prepended to the dotted dataset path in the URN, for
	// ingestion sources that qualify tables with a catalog. DataHub's trino
	// source names a dataset "<catalog>.<schema>.<table>", so a Polaris
	// catalog mounted in Trino as "polaris" needs DatasetPrefix "polaris".
	// The iceberg and hive sources use "<schema>.<table>", so leave it empty.
	DatasetPrefix string
	// Env is the DataHub fabric type in dataset URNs. Defaults to PROD.
	Env string
	// Timeout bounds a single GraphQL call. Default 30s.
	Timeout time.Duration
	// SensitiveTags are the tag or glossary-term names that mark a column as
	// sensitive. Matching is case-insensitive on the tag's name. Defaults to
	// a conservative PII set.
	SensitiveTags []string
}

// Client is a DataHub GraphQL client implementing catalog.Enricher.
type Client struct {
	http      *http.Client
	url       string
	token     string
	platform  string
	prefix    string
	env       string
	sensitive map[string]bool
}

var defaultSensitiveTags = []string{"pii", "sensitive", "confidential", "restricted"}

// New builds a client. The URL and platform are required.
func New(opts Options) (*Client, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("datahub: URL is required")
	}
	if opts.Platform == "" {
		return nil, fmt.Errorf("datahub: platform is required (e.g. iceberg, trino, hive)")
	}
	if opts.Env == "" {
		opts.Env = "PROD"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	tags := opts.SensitiveTags
	if len(tags) == 0 {
		tags = defaultSensitiveTags
	}
	sensitive := make(map[string]bool, len(tags))
	for _, t := range tags {
		sensitive[strings.ToLower(strings.TrimSpace(t))] = true
	}
	return &Client{
		http:      &http.Client{Timeout: opts.Timeout},
		url:       strings.TrimRight(opts.URL, "/"),
		token:     opts.Token,
		platform:  opts.Platform,
		prefix:    strings.Trim(opts.DatasetPrefix, "."),
		env:       opts.Env,
		sensitive: sensitive,
	}, nil
}

// DescribeTables fetches metadata for the named tables. Tables DataHub does
// not know about are omitted from the result rather than reported as errors:
// a partially catalogued database still enriches the part it knows.
//
// One batched GraphQL call covers every table, because a real database has
// hundreds of them and a call per table would dominate derivation time. If
// the server rejects the batch query (an older GraphQL schema without the
// entities root field), it falls back to fetching one dataset at a time.
func (c *Client) DescribeTables(ctx context.Context, database string, tables []string) (map[string]catalog.TableMeta, error) {
	if len(tables) == 0 {
		return map[string]catalog.TableMeta{}, nil
	}
	byURN := make(map[string]string, len(tables))
	urns := make([]string, 0, len(tables))
	for _, t := range tables {
		u := c.urn(database, t)
		byURN[u] = t
		urns = append(urns, u)
	}

	out, err := c.fetchBatch(ctx, urns, byURN)
	if err == nil {
		return out, nil
	}
	if !isUnsupportedQuery(err) {
		return nil, fmt.Errorf("datahub: %w", err)
	}
	return c.describeOneByOne(ctx, database, tables)
}

func (c *Client) describeOneByOne(ctx context.Context, database string, tables []string) (map[string]catalog.TableMeta, error) {
	out := make(map[string]catalog.TableMeta, len(tables))
	for _, t := range tables {
		ds, err := c.fetchDataset(ctx, c.urn(database, t))
		if err != nil {
			return nil, fmt.Errorf("datahub: %s.%s: %w", database, t, err)
		}
		if ds == nil {
			continue // not catalogued upstream
		}
		out[t] = c.toTableMeta(ds)
	}
	return out, nil
}

// isUnsupportedQuery reports whether a GraphQL error means the server does
// not implement the batch query, as opposed to a real failure. Only then is
// falling back to per-dataset calls correct; anything else must surface.
func isUnsupportedQuery(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "validationerror") ||
		strings.Contains(m, "field 'entities'") ||
		strings.Contains(m, "unknown field") ||
		strings.Contains(m, "cannot query field")
}

// urn builds the DataHub dataset URN for a table. DataHub names a dataset by
// its dotted path within the platform, which the ingestion source decides:
// "<schema>.<table>" for iceberg and hive, "<catalog>.<schema>.<table>" for
// trino. DatasetPrefix supplies the leading component when there is one.
func (c *Client) urn(database, table string) string {
	path := database + "." + table
	if c.prefix != "" {
		path = c.prefix + "." + path
	}
	return fmt.Sprintf("urn:li:dataset:(urn:li:dataPlatform:%s,%s,%s)",
		c.platform, path, c.env)
}

// datasetQuery asks for exactly the fields enrichment consumes: dataset-level
// documentation and tags, and per-column description, tags, and glossary
// terms. Anything else DataHub knows is deliberately not requested.
// DataHub keeps two layers of metadata. Ingestion writes properties and
// schemaMetadata; a steward editing in the UI writes editableProperties and
// editableSchemaMetadata instead, leaving the ingested layer untouched. The
// curated layer is the one worth importing, so both are requested and the
// editable one wins.
const datasetFields = `
    properties { description }
    editableProperties { description }
    deprecation { deprecated }
    globalTags { tags { tag { properties { name } } } }
    glossaryTerms { terms { term { properties { name } } } }
    schemaMetadata {
      fields {
        fieldPath
        description
        globalTags { tags { tag { properties { name } } } }
        glossaryTerms { terms { term { properties { name } } } }
      }
    }
    editableSchemaMetadata {
      editableSchemaFieldInfo {
        fieldPath
        description
        globalTags { tags { tag { properties { name } } } }
        glossaryTerms { terms { term { properties { name } } } }
      }
    }`

const datasetQuery = `query($urn: String!) {
  dataset(urn: $urn) {` + datasetFields + `
  }
}`

// batchQuery fetches every requested dataset in one call. Non-dataset
// entities and unknown URNs come back as nulls, which are skipped.
const batchQuery = `query($urns: [String!]!) {
  entities(urns: $urns) {
    urn
    ... on Dataset {` + datasetFields + `
    }
  }
}`

type gqlErrors struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type gqlResponse struct {
	Data struct {
		Dataset *dataset `json:"dataset"`
	} `json:"data"`
}

// batchEntity is a dataset plus the URN that identifies which one it is.
type batchEntity struct {
	URN string `json:"urn"`
	dataset
}

type batchResponse struct {
	Data struct {
		Entities []*batchEntity `json:"entities"`
	} `json:"data"`
}

// fieldInfo is one column's metadata, in either the ingested or the
// steward-edited layer (both use the same shape).
type fieldInfo struct {
	FieldPath     string    `json:"fieldPath"`
	Description   string    `json:"description"`
	GlobalTags    *tagList  `json:"globalTags"`
	GlossaryTerms *termList `json:"glossaryTerms"`
}

type description struct {
	Description string `json:"description"`
}

type dataset struct {
	Properties         *description `json:"properties"`
	EditableProperties *description `json:"editableProperties"`
	Deprecation        *struct {
		Deprecated bool `json:"deprecated"`
	} `json:"deprecation"`
	GlobalTags     *tagList  `json:"globalTags"`
	GlossaryTerms  *termList `json:"glossaryTerms"`
	SchemaMetadata *struct {
		Fields []fieldInfo `json:"fields"`
	} `json:"schemaMetadata"`
	EditableSchemaMetadata *struct {
		EditableSchemaFieldInfo []fieldInfo `json:"editableSchemaFieldInfo"`
	} `json:"editableSchemaMetadata"`
}

// isEmpty reports whether DataHub returned a placeholder with no metadata at
// all, which is how it represents a dataset it has never ingested.
func (d *dataset) isEmpty() bool {
	return d.Properties == nil && d.EditableProperties == nil && d.Deprecation == nil &&
		d.GlobalTags == nil && d.GlossaryTerms == nil &&
		d.SchemaMetadata == nil && d.EditableSchemaMetadata == nil
}

type tagList struct {
	Tags []struct {
		Tag struct {
			Properties *struct {
				Name string `json:"name"`
			} `json:"properties"`
		} `json:"tag"`
	} `json:"tags"`
}

type termList struct {
	Terms []struct {
		Term struct {
			Properties *struct {
				Name string `json:"name"`
			} `json:"properties"`
		} `json:"term"`
	} `json:"terms"`
}

// fetchBatch resolves every URN in one call and maps the results back to
// table names through byURN.
func (c *Client) fetchBatch(ctx context.Context, urns []string, byURN map[string]string) (map[string]catalog.TableMeta, error) {
	var out batchResponse
	if err := c.post(ctx, batchQuery, map[string]any{"urns": urns}, &out); err != nil {
		return nil, err
	}
	meta := make(map[string]catalog.TableMeta, len(urns))
	for _, e := range out.Data.Entities {
		if e == nil {
			continue // unknown URN
		}
		table, ok := byURN[e.URN]
		if !ok {
			continue
		}
		// DataHub answers an uncatalogued URN with a stub entity rather than
		// null, so an absent dataset arrives as every field nil. Skipping
		// those keeps the reported enrichment count honest.
		if e.isEmpty() {
			continue
		}
		meta[table] = c.toTableMeta(&e.dataset)
	}
	return meta, nil
}

func (c *Client) fetchDataset(ctx context.Context, urn string) (*dataset, error) {
	var out gqlResponse
	if err := c.post(ctx, datasetQuery, map[string]any{"urn": urn}, &out); err != nil {
		return nil, err
	}
	return out.Data.Dataset, nil
}

// post executes one GraphQL request and decodes it into v.
func (c *Client) post(ctx context.Context, query string, variables map[string]any, v any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/api/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", authHeader(c.token))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	// Decode errors first: GraphQL reports them with HTTP 200 and a partial
	// or absent data field.
	var errs gqlErrors
	if err := json.Unmarshal(raw, &errs); err == nil && len(errs.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", errs.Errors[0].Message)
	}
	return json.Unmarshal(raw, v)
}

// maxResponseBytes bounds a GraphQL response so a large or hostile catalog
// cannot exhaust memory during derivation.
const maxResponseBytes = 64 << 20

// authHeader renders the Authorization value. A personal access token is a
// bare string and becomes a bearer token; a value that already carries a
// scheme (such as DataHub's "Basic <clientId>:<clientSecret>" system
// credential) is passed through untouched.
func authHeader(token string) string {
	switch {
	case strings.HasPrefix(token, "Bearer "), strings.HasPrefix(token, "Basic "):
		return token
	default:
		return "Bearer " + token
	}
}

func (c *Client) toTableMeta(ds *dataset) catalog.TableMeta {
	meta := catalog.TableMeta{Fields: map[string]catalog.FieldMeta{}}
	// A steward's edit wins over whatever ingestion recorded.
	if ds.Properties != nil {
		meta.Description = ds.Properties.Description
	}
	if ds.EditableProperties != nil && ds.EditableProperties.Description != "" {
		meta.Description = ds.EditableProperties.Description
	}
	if ds.Deprecation != nil {
		meta.Deprecated = ds.Deprecation.Deprecated
	}
	// Glossary terms are curated business vocabulary, which is exactly what
	// an agent needs to ground a user's words. Tags are classification, so
	// they feed governance instead.
	meta.Synonyms = termNames(ds.GlossaryTerms)

	// Merge the ingested and edited column layers, edited last so it wins.
	if ds.SchemaMetadata != nil {
		c.mergeFields(meta.Fields, ds.SchemaMetadata.Fields)
	}
	if ds.EditableSchemaMetadata != nil {
		c.mergeFields(meta.Fields, ds.EditableSchemaMetadata.EditableSchemaFieldInfo)
	}
	return meta
}

// mergeFields folds one metadata layer into the accumulated column map. A
// later layer overrides a description it actually provides, and sensitivity
// is sticky: a classification in either layer marks the column.
func (c *Client) mergeFields(into map[string]catalog.FieldMeta, fields []fieldInfo) {
	for _, f := range fields {
		name := columnName(f.FieldPath)
		if name == "" {
			continue
		}
		cur := into[name]
		if f.Description != "" {
			cur.Description = f.Description
		}
		if terms := termNames(f.GlossaryTerms); len(terms) > 0 {
			cur.Synonyms = terms
		}
		if c.anySensitive(tagNames(f.GlobalTags), termNames(f.GlossaryTerms)) {
			cur.Sensitive = true
		}
		into[name] = cur
	}
}

func (c *Client) anySensitive(groups ...[]string) bool {
	for _, g := range groups {
		for _, n := range g {
			if c.sensitive[strings.ToLower(n)] {
				return true
			}
		}
	}
	return false
}

// columnName strips DataHub's v2 fieldPath encoding
// ("[version=2.0].[type=struct].[type=string].c_email_address") down to the
// column name, and ignores nested fields, which have no flat column.
func columnName(fieldPath string) string {
	if !strings.Contains(fieldPath, "[") {
		if strings.Contains(fieldPath, ".") {
			return "" // nested field of a struct column
		}
		return fieldPath
	}
	parts := strings.Split(fieldPath, "].")
	last := parts[len(parts)-1]
	if strings.Contains(last, ".") {
		return "" // nested
	}
	return last
}

func tagNames(l *tagList) []string {
	if l == nil {
		return nil
	}
	var out []string
	for _, t := range l.Tags {
		if t.Tag.Properties != nil && t.Tag.Properties.Name != "" {
			out = append(out, t.Tag.Properties.Name)
		}
	}
	return out
}

func termNames(l *termList) []string {
	if l == nil {
		return nil
	}
	var out []string
	for _, t := range l.Terms {
		if t.Term.Properties != nil && t.Term.Properties.Name != "" {
			out = append(out, t.Term.Properties.Name)
		}
	}
	return out
}
