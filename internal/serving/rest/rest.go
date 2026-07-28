// Package rest is the JSON adapter for custom UIs. It contains no query
// logic: every endpoint translates HTTP to serving.Service calls.
package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving"
	"github.com/KubedAI/semantic-operator/internal/serving/auth"
)

// RoleHeader names the identity header. It is trusted only when the
// authenticator runs in header mode; see internal/serving/auth.
const RoleHeader = auth.RoleHeader

// Handler mounts the REST API onto a mux. The authenticator resolves the
// caller for every request; a nil authenticator falls back to header mode so
// tests and embedders keep working.
func Handler(svc *serving.Service, authn *auth.Authenticator) http.Handler {
	mux := http.NewServeMux()
	// Discovery is authenticated for the same reason querying is. The listing
	// names every certified metric and every column, so an unauthenticated
	// caller would learn the shape of the warehouse without running a query.
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		id, err := identity(authn, r)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": svc.Models(id)})
	})
	mux.HandleFunc("GET /v1/models/{model}/metrics", func(w http.ResponseWriter, r *http.Request) {
		m, id, err := resolve(svc, authn, r)
		if err != nil {
			writeErr(w, err)
			return
		}
		metrics, err := svc.ListMetrics(m, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"model": m.Name, "metrics": metrics})
	})
	mux.HandleFunc("GET /v1/models/{model}/dimensions", func(w http.ResponseWriter, r *http.Request) {
		m, id, err := resolve(svc, authn, r)
		if err != nil {
			writeErr(w, err)
			return
		}
		dims, err := svc.ListDimensions(m, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"model": m.Name, "dimensions": dims})
	})
	mux.HandleFunc("POST /v1/models/{model}/query", func(w http.ResponseWriter, r *http.Request) {
		handleQuery(svc, authn, w, r, true)
	})
	mux.HandleFunc("POST /v1/models/{model}/sql", func(w http.ResponseWriter, r *http.Request) {
		handleQuery(svc, authn, w, r, false)
	})
	return mux
}

// resolve authenticates the caller and then looks up the model. The order
// matters: resolving first would let an unauthenticated caller tell a real
// model name from a made-up one by the difference between 404 and 401.
func resolve(svc *serving.Service, authn *auth.Authenticator, r *http.Request) (*planner.CompiledModel, governance.Identity, error) {
	id, err := identity(authn, r)
	if err != nil {
		return nil, governance.Identity{}, err
	}
	m, err := svc.Resolve(r.PathValue("model"))
	if err != nil {
		return nil, governance.Identity{}, err
	}
	return m, id, nil
}

func handleQuery(svc *serving.Service, authn *auth.Authenticator, w http.ResponseWriter, r *http.Request, execute bool) {
	m, id, err := resolve(svc, authn, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req planner.Request
	if err := decodeJSON(w, r, svc.MaxRequestBytes(), &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !execute {
		plan, cached, err := svc.Plan(r.Context(), m, req, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "cachedPlan": cached})
		return
	}
	res, err := svc.Query(r.Context(), "rest", m, req, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// decodeJSON reads exactly one JSON document into dst under a byte ceiling.
//
// Three things beyond a plain Decode:
//
// MaxBytesReader bounds the body, so an unbounded upload cannot be buffered
// into a pod with a fixed memory limit.
//
// DisallowUnknownFields turns a misspelled field into an error instead of a
// silent omission. Sending "dimension" for "dimensions" used to compile and
// run a different query than the caller wrote, and return a confidently wrong
// answer. That is the failure this project exists to prevent, so it is worth a
// 400.
//
// The trailing-document check rejects a body holding more than one JSON value,
// which would otherwise be silently ignored after the first.
func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("request body exceeds the %d byte maximum", maxBytes)
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if dec.More() {
		return errors.New("body must contain exactly one JSON object")
	}
	return nil
}

// identity resolves the caller, defaulting to header mode when no
// authenticator was supplied.
func identity(authn *auth.Authenticator, r *http.Request) (governance.Identity, error) {
	if authn == nil {
		return governance.Single(r.Header.Get(RoleHeader)), nil
	}
	return authn.Identity(r)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var unknown serving.ErrUnknownModel
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		status = http.StatusUnauthorized
	case errors.Is(err, governance.ErrUnauthorized):
		status = http.StatusForbidden
	case errors.As(err, &unknown):
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
