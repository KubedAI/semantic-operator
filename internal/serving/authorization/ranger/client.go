package ranger

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

	"golang.org/x/net/http/httpguts"
)

const (
	defaultTimeout          = 2 * time.Second
	defaultMaxResponseBytes = int64(1 << 20)
)

// Options configures a standalone ranger PDP HTTP client. URL is the complete
// API base to which authorize, authorizeMulti, and permissions are appended.
// Standard ranger PDP deployments use a base ending in /authz/v1.
type Options struct {
	URL              string
	BearerToken      string
	Headers          map[string]string
	Timeout          time.Duration
	MaxResponseBytes int64
	HTTPClient       *http.Client
}

// Client calls the three ranger PDP REST authorization endpoints.
type Client struct {
	authorizeURL      string
	authorizeMultiURL string
	permissionsURL    string
	bearerToken       string
	headers           map[string]string
	timeout           time.Duration
	maxResponseBytes  int64
	http              *http.Client
}

// HTTPError reports a non-2xx response from ranger PDP without exposing an
// unvalidated response body.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "ranger PDP HTTP error"
	}
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("ranger PDP returned HTTP %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("ranger PDP returned HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("ranger PDP returned HTTP %d", e.StatusCode)
}

// New validates PDP transport configuration and constructs endpoint URLs.
func New(opts Options) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(opts.URL))
	if err != nil {
		return nil, fmt.Errorf("parsing ranger PDP URL: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("ranger PDP URL scheme %q is not supported: use http or https", base.Scheme)
	}
	if base.Host == "" {
		return nil, errors.New("ranger PDP URL must include a host")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("ranger PDP URL must not contain user info, a query, or a fragment")
	}
	if strings.ContainsAny(opts.BearerToken, "\r\n") {
		return nil, errors.New("ranger PDP bearer token must not contain a line break")
	}

	headers := make(map[string]string, len(opts.Headers))
	if (opts.BearerToken != "" || len(opts.Headers) != 0) && base.Scheme != "https" {
		return nil, errors.New("ranger PDP URL must use https when credentials or trusted headers are configured")
	}
	for name, value := range opts.Headers {
		if !validHeaderName(name) {
			return nil, fmt.Errorf("invalid ranger PDP header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("invalid ranger PDP header value for %q", name)
		}
		canonicalName := http.CanonicalHeaderKey(name)
		switch canonicalName {
		case "Accept", "Authorization", "Content-Type", "Host":
			return nil, fmt.Errorf("ranger PDP header %q is managed by the client", name)
		}
		if _, exists := headers[canonicalName]; exists {
			return nil, fmt.Errorf("ranger PDP header %q is configured more than once", canonicalName)
		}
		headers[canonicalName] = value
	}

	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	baseURL := base.String()
	authorizeURL, err := url.JoinPath(baseURL, "authorize")
	if err != nil {
		return nil, fmt.Errorf("building ranger PDP authorize URL: %w", err)
	}
	authorizeMultiURL, err := url.JoinPath(baseURL, "authorizeMulti")
	if err != nil {
		return nil, fmt.Errorf("building ranger PDP authorizeMulti URL: %w", err)
	}
	permissionsURL, err := url.JoinPath(baseURL, "permissions")
	if err != nil {
		return nil, fmt.Errorf("building ranger PDP permissions URL: %w", err)
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
		authorizeURL: authorizeURL, authorizeMultiURL: authorizeMultiURL,
		permissionsURL: permissionsURL, bearerToken: opts.BearerToken,
		headers: headers, timeout: opts.Timeout,
		maxResponseBytes: opts.MaxResponseBytes, http: &httpClientCopy,
	}, nil
}

// Authorize evaluates one ranger access request.
func (c *Client) Authorize(ctx context.Context, request AuthorizationRequest) (AuthorizationResult, error) {
	var result AuthorizationResult
	err := c.post(ctx, c.authorizeURL, request, &result)
	return result, err
}

// AuthorizeMulti evaluates multiple ranger access requests in one call.
func (c *Client) AuthorizeMulti(ctx context.Context, request MultiAuthorizationRequest) (MultiAuthorizationResult, error) {
	var result MultiAuthorizationResult
	err := c.post(ctx, c.authorizeMultiURL, request, &result)
	return result, err
}

// Permissions returns effective permissions for a ranger resource.
func (c *Client) Permissions(ctx context.Context, request ResourcePermissionsRequest) (ResourcePermissions, error) {
	var result ResourcePermissions
	err := c.post(ctx, c.permissionsURL, request, &result)
	return result, err
}

func (c *Client) post(ctx context.Context, endpoint string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding ranger PDP request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating ranger PDP request: %w", err)
	}
	for name, value := range c.headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling ranger PDP: %w", err)
	}
	defer resp.Body.Close()
	readLimit := c.maxResponseBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	blob, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return fmt.Errorf("reading ranger PDP response: %w", err)
	}
	if int64(len(blob)) > c.maxResponseBytes {
		return fmt.Errorf("ranger PDP response exceeds %d bytes", c.maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return decodeHTTPError(resp.StatusCode, blob)
	}
	if err := decodeStrictJSON(blob, result); err != nil {
		return fmt.Errorf("decoding ranger PDP response: %w", err)
	}
	return nil
}

func decodeHTTPError(status int, blob []byte) error {
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := decodeStrictJSON(blob, &body); err != nil {
		return &HTTPError{StatusCode: status}
	}
	return &HTTPError{StatusCode: status, Code: body.Code, Message: body.Message}
}

func decodeStrictJSON(blob []byte, target any) error {
	if bytes.Equal(bytes.TrimSpace(blob), []byte("null")) {
		return errors.New("response must be a JSON object, not null")
	}
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("response must contain exactly one JSON value")
		}
		return fmt.Errorf("response must contain exactly one JSON value: %w", err)
	}
	return nil
}

func validHeaderName(name string) bool {
	return httpguts.ValidHeaderFieldName(name)
}
