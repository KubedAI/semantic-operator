//go:build e2e

// Package authe2e is the auth end-to-end suite. It runs only under the e2e
// build tag, so the offline unit suite never compiles or runs it.
//
// Connectivity uses kubectl port-forward, not the API server proxy. The API
// server proxy strips the Authorization header before it reaches the backend,
// so the jwt server would never see the caller token. To keep port-forward
// reliable the harness waits for each local port to accept a connection before
// use, and retries each request a few times.
//
// Every value is overridable by env, so the same suite runs against either
// engine; the orchestrator (make e2e) deploys the matrix per KIND_ENGINE_TYPE.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

type config struct {
	kubeconfig string
	context    string

	keycloakNS   string
	keycloakSvc  string
	keycloakPort int
	realm        string
	clientID     string
	password     string

	// One server release per engine identity mode, plus the governance release.
	staticNS      string
	passthroughNS string
	exchangeNS    string
	govNS         string
	serverSvc     string
	serverPort    int

	model         string
	localPortBase int

	// Query shape differs per engine. maskDim is empty when the engine cannot
	// mask (StarRocks), which skips the mask assertion.
	engine      string
	metric      string
	ratioMetric string
	allowDim    string // a declared dimension not on the denied table
	denyDim     string // a declared dimension on the denied table
	maskDim     string // the masked declared dimension, or "" to skip masking
	nonDim      string // a readable field with no Ossie dimension declaration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func loadConfig() config {
	// The engine profile is the single source for the query shape. An unknown
	// engine falls back to trino so the suite still loads.
	engine := env("E2E_ENGINE", "trino")
	p, ok := profiles[engine]
	if !ok {
		engine, p = "trino", profiles["trino"]
	}
	return config{
		kubeconfig:    os.Getenv("E2E_KUBECONFIG"),
		context:       env("E2E_CONTEXT", "kind-semantic-operator-dev"),
		keycloakNS:    env("E2E_KEYCLOAK_NS", "semantic-system"),
		keycloakSvc:   env("E2E_KEYCLOAK_SVC", "keycloak"),
		keycloakPort:  8080,
		realm:         env("E2E_REALM", "semantic"),
		clientID:      env("E2E_CLIENT_ID", "semantic-cli"),
		password:      env("E2E_PASSWORD", "password"),
		staticNS:      env("E2E_STATIC_NS", "sem-static"),
		passthroughNS: env("E2E_PASSTHROUGH_NS", "sem-passthrough"),
		exchangeNS:    env("E2E_EXCHANGE_NS", "sem-exchange"),
		govNS:         env("E2E_GOV_NS", "semantic-system"),
		serverSvc:     env("E2E_SERVER_SVC", "semantic-operator-server"),
		serverPort:    8090,
		model:         env("E2E_MODEL", p.modelName),
		localPortBase: envInt("E2E_LOCAL_PORT_BASE", 19001),
		engine:        engine,
		metric:        env("E2E_METRIC", p.metric),
		ratioMetric:   env("E2E_RATIO_METRIC", p.ratioMetric),
		allowDim:      env("E2E_ALLOW_DIM", p.allowDim),
		denyDim:       env("E2E_DENY_DIM", p.denyDim),
		maskDim:       env("E2E_MASK_DIM", p.maskDim),
		nonDim:        env("E2E_NON_DIM", p.nonDim),
	}
}

func query(dims ...string) map[string]any {
	q := map[string]any{"metrics": []string{cfg.metric}, "limit": 1}
	if len(dims) > 0 {
		q["dimensions"] = dims
	}
	return q
}

func minimalQuery() map[string]any { return query() }
func allowQuery() map[string]any   { return query(cfg.allowDim) }
func denyQuery() map[string]any    { return query(cfg.denyDim) }
func maskQuery() map[string]any    { return query(cfg.maskDim) }

// rowsClose reports whether two row sets hold the same values. It compares
// numbers within a relative tolerance and all other cells exactly. The
// tolerance absorbs the last-bit drift of a parallel DOUBLE SUM. The engine
// adds the column across splits in an order that is not fixed, so two runs of
// the same aggregate can differ in the last bit.
func rowsClose(a, b [][]any, relTol float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if !cellsClose(a[i][j], b[i][j], relTol) {
				return false
			}
		}
	}
	return true
}

// cellsClose compares two decoded JSON cells. Numbers decode to float64 and are
// compared within relTol. All other cells are compared by their text form.
func cellsClose(x, y any, relTol float64) bool {
	fx, okx := x.(float64)
	fy, oky := y.(float64)
	if okx && oky {
		if fx == fy {
			return true
		}
		scale := math.Max(math.Abs(fx), math.Abs(fy))
		return math.Abs(fx-fy) <= relTol*scale
	}
	return fmt.Sprintf("%v", x) == fmt.Sprintf("%v", y)
}

var (
	cfg        config
	httpClient = &http.Client{Timeout: 30 * time.Second}

	pfMu      sync.Mutex
	forwards  = map[string]int{} // ns/svc/port -> local port
	pfProcs   []*exec.Cmd
	nextLocal int
)

func TestMain(m *testing.M) {
	cfg = loadConfig()
	nextLocal = cfg.localPortBase
	// Optional deploy step. Off by default so the assertion loop is fast; make
	// e2e sets it to stand up the matrix first.
	if os.Getenv("E2E_SETUP") != "" {
		if err := setupCluster(profiles[cfg.engine]); err != nil {
			fmt.Fprintf(os.Stderr, "e2e setup: %v\n", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	stopForwards()
	os.Exit(code)
}

func stopForwards() {
	pfMu.Lock()
	defer pfMu.Unlock()
	for _, c := range pfProcs {
		if c.Process != nil {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}
}

// ensureForward starts one kubectl port-forward per Service and returns the
// local port. Forwards are cached for the run and torn down in TestMain.
func ensureForward(ns, svc string, remote int) (int, error) {
	key := fmt.Sprintf("%s/%s/%d", ns, svc, remote)
	pfMu.Lock()
	defer pfMu.Unlock()
	if p, ok := forwards[key]; ok {
		return p, nil
	}
	local := nextLocal
	nextLocal++

	args := []string{}
	if cfg.kubeconfig != "" {
		args = append(args, "--kubeconfig", cfg.kubeconfig)
	}
	if cfg.context != "" {
		args = append(args, "--context", cfg.context)
	}
	args = append(args, "-n", ns, "port-forward", "service/"+svc,
		fmt.Sprintf("%d:%d", local, remote), "--address", "127.0.0.1")

	cmd := exec.Command("kubectl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start port-forward %s: %w", key, err)
	}
	pfProcs = append(pfProcs, cmd)

	if err := waitPort(local, 15*time.Second); err != nil {
		return 0, fmt.Errorf("port-forward %s not ready: %w (kubectl: %s)", key, err, stderr.String())
	}
	forwards[key] = local
	return local, nil
}

func waitPort(port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

func serverBaseURL(ns string) (string, error) {
	local, err := ensureForward(ns, cfg.serverSvc, cfg.serverPort)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", local), nil
}

func keycloakBaseURL() (string, error) {
	local, err := ensureForward(cfg.keycloakNS, cfg.keycloakSvc, cfg.keycloakPort)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", local), nil
}

// doHTTP issues a request and retries a few times, so a forward that has just
// come up or briefly dropped does not fail the test.
func doHTTP(ctx context.Context, method, baseURL, path string, headers map[string]string, body []byte, contentType string) (int, []byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, rdr)
		if err != nil {
			return 0, nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, raw, nil
	}
	return 0, nil, lastErr
}

// mintToken gets a Keycloak access token for a user by the password grant.
func mintToken(ctx context.Context, user string) (string, error) {
	base, err := keycloakBaseURL()
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id":  {cfg.clientID},
		"grant_type": {"password"},
		"username":   {user},
		"password":   {cfg.password},
	}
	path := fmt.Sprintf("/realms/%s/protocol/openid-connect/token", cfg.realm)
	code, raw, err := doHTTP(ctx, "POST", base, path, nil, []byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("token endpoint returned %d: %s", code, raw)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in response: %s", raw)
	}
	return out.AccessToken, nil
}

type queryResult struct {
	status  int
	columns []string
	rows    [][]any
	sql     string
	errMsg  string
	raw     []byte
}

// masked reports whether any returned cell equals the sentinel mask value.
func (r queryResult) masked() bool {
	for _, row := range r.rows {
		for _, cell := range row {
			if s, ok := cell.(string); ok && s == "REDACTED" {
				return true
			}
		}
	}
	return false
}

// queryModel POSTs a query to a server release and parses the response.
func queryModel(ctx context.Context, ns, model string, headers map[string]string, body map[string]any) (queryResult, error) {
	base, err := serverBaseURL(ns)
	if err != nil {
		return queryResult{}, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return queryResult{}, err
	}
	path := fmt.Sprintf("/v1/models/%s/query", model)
	code, raw, err := doHTTP(ctx, "POST", base, path, headers, b, "application/json")
	if err != nil {
		return queryResult{}, err
	}
	r := queryResult{status: code, raw: raw}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) == nil {
		if e, ok := parsed["error"]; ok {
			var s string
			if json.Unmarshal(e, &s) == nil && s != "" {
				r.errMsg = s
			} else {
				r.errMsg = string(e)
			}
		}
		if rw, ok := parsed["rows"]; ok {
			_ = json.Unmarshal(rw, &r.rows)
		}
		if cw, ok := parsed["columns"]; ok {
			_ = json.Unmarshal(cw, &r.columns)
		}
		if sw, ok := parsed["sql"]; ok {
			_ = json.Unmarshal(sw, &r.sql)
		}
	}
	return r, nil
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func token(t *testing.T, user string) string {
	t.Helper()
	tok, err := mintToken(testCtx(t), user)
	if err != nil {
		t.Fatalf("mint token for %s: %v", user, err)
	}
	return tok
}
