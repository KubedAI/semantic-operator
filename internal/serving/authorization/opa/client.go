// Package opa implements external authorization through OPA's Data API.
package opa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
)

const (
	defaultTimeout          = 2 * time.Second
	defaultMaxResponseBytes = int64(1 << 20)
)

// Options configures one OPA provider. DecisionPath is appended below
// /v1/data/ once during construction.
type Options struct {
	URL              string
	DecisionPath     string
	BearerToken      string
	Timeout          time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
}

// Client evaluates one configured OPA decision.
type Client struct {
	decisionURL      string
	bearerToken      string
	timeout          time.Duration
	maxResponseBytes int64
	http             *http.Client
}

// New validates URL configuration and constructs an OPA provider.
func New(opts Options) (*Client, error) {
	base, err := url.Parse(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing OPA URL: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("OPA URL scheme %q is not supported: use http or https", base.Scheme)
	}
	if base.Host == "" {
		return nil, errors.New("OPA URL must include a host")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("OPA URL must not contain user info, a query, or a fragment")
	}
	parts, err := decisionPathParts(opts.DecisionPath)
	if opts.BearerToken != "" && base.Scheme != "https" {
		return nil, errors.New("OPA URL must use https when a bearer token is configured")
	}
	if err != nil {
		return nil, err
	}
	path := append([]string{"v1", "data"}, parts...)
	decisionURL, err := url.JoinPath(base.String(), path...)
	if err != nil {
		return nil, fmt.Errorf("building OPA decision URL: %w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = defaultMaxResponseBytes
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpClientCopy := *httpClient
	httpClientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		decisionURL: decisionURL, bearerToken: opts.BearerToken,
		timeout: opts.Timeout, maxResponseBytes: opts.MaxResponseBytes,
		http: &httpClientCopy,
	}, nil
}

// Decide posts the semantic input to the configured OPA Data API document.
// Accepted results are deliberately narrow: a boolean, or
// {"allow":bool,"revision":"..."}. Other obligations are rejected rather
// than silently ignored.
func (c *Client) Decide(ctx context.Context, input authorization.Input) (authorization.Decision, error) {
	body, err := json.Marshal(struct {
		Input authorization.Input `json:"input"`
	}{Input: input})
	if err != nil {
		return authorization.Decision{}, fmt.Errorf("encoding OPA input: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.decisionURL, bytes.NewReader(body))
	if err != nil {
		return authorization.Decision{}, fmt.Errorf("creating OPA request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return authorization.Decision{}, fmt.Errorf("calling OPA: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	readLimit := c.maxResponseBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	blob, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return authorization.Decision{}, fmt.Errorf("reading OPA response: %w", err)
	}
	if int64(len(blob)) > c.maxResponseBytes {
		return authorization.Decision{}, fmt.Errorf("OPA response exceeds %d bytes", c.maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return authorization.Decision{}, fmt.Errorf("OPA returned HTTP %d", resp.StatusCode)
	}
	return decodeDecision(blob)
}

type envelope struct {
	DecisionID string          `json:"decision_id,omitempty"`
	Result     json.RawMessage `json:"result"`
}

type objectDecision struct {
	Allow    *bool  `json:"allow"`
	Revision string `json:"revision,omitempty"`
}

func decodeDecision(blob []byte) (authorization.Decision, error) {
	if bytes.Equal(bytes.TrimSpace(blob), []byte("null")) {
		return authorization.Decision{}, errors.New("decoding OPA response: response must be an object, not null")
	}
	var env envelope
	envelopeDecoder := json.NewDecoder(bytes.NewReader(blob))
	envelopeDecoder.DisallowUnknownFields()
	if err := envelopeDecoder.Decode(&env); err != nil {
		return authorization.Decision{}, fmt.Errorf("decoding OPA response: %w", err)
	}
	if err := envelopeDecoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return authorization.Decision{}, errors.New("decoding OPA response: response must contain exactly one JSON value")
		}
		return authorization.Decision{}, fmt.Errorf("decoding OPA response: response must contain exactly one JSON value: %w", err)
	}
	result := bytes.TrimSpace(env.Result)
	// OPA returns an empty object when a data document is undefined. Undefined
	// is a deny, not an infrastructure error.
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return authorization.Decision{Allow: false}, nil
	}
	var allow bool
	if err := json.Unmarshal(result, &allow); err == nil {
		return authorization.Decision{Allow: allow}, nil
	}

	dec := objectDecision{}
	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dec); err != nil {
		return authorization.Decision{}, fmt.Errorf("OPA result must be a boolean or an object containing only allow and revision: %w", err)
	}
	if dec.Allow == nil {
		return authorization.Decision{}, errors.New("OPA object result is missing required allow")
	}
	if len(dec.Revision) > 256 {
		return authorization.Decision{}, errors.New("OPA policy revision exceeds 256 characters")
	}
	for _, r := range dec.Revision {
		if unicode.IsControl(r) {
			return authorization.Decision{}, errors.New("OPA policy revision contains a control character")
		}
	}
	return authorization.Decision{Allow: *dec.Allow, Revision: dec.Revision}, nil
}

func decisionPathParts(decisionPath string) ([]string, error) {
	if len(decisionPath) == 0 || len(decisionPath) > 512 {
		return nil, errors.New("OPA decisionPath must contain 1 to 512 characters")
	}
	parts := strings.Split(decisionPath, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid OPA decisionPath %q", decisionPath)
		}
		for _, c := range part {
			if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') &&
				(c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
				return nil, fmt.Errorf("invalid OPA decisionPath %q", decisionPath)
			}
		}
	}
	return parts, nil
}
