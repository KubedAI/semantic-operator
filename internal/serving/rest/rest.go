// Package rest is the JSON adapter for custom UIs. It contains no query
// logic: every endpoint translates HTTP to serving.Service calls.
package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/KubedAI/ossie-semantic-operator/internal/governance"
	"github.com/KubedAI/ossie-semantic-operator/internal/planner"
	"github.com/KubedAI/ossie-semantic-operator/internal/serving"
)

// RoleHeader names the trusted identity header. Deployments are expected to
// terminate authentication in front of the service (see ARCHITECTURE.md).
const RoleHeader = "X-Semantic-Role"

// Handler mounts the REST API onto a mux.
func Handler(svc *serving.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"models": svc.Models()})
	})
	mux.HandleFunc("GET /v1/models/{model}/metrics", func(w http.ResponseWriter, r *http.Request) {
		m, err := svc.Resolve(r.PathValue("model"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"model": m.Name, "metrics": svc.ListMetrics(m)})
	})
	mux.HandleFunc("GET /v1/models/{model}/dimensions", func(w http.ResponseWriter, r *http.Request) {
		m, err := svc.Resolve(r.PathValue("model"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"model": m.Name, "dimensions": svc.ListDimensions(m)})
	})
	mux.HandleFunc("POST /v1/models/{model}/query", func(w http.ResponseWriter, r *http.Request) {
		handleQuery(svc, w, r, true)
	})
	mux.HandleFunc("POST /v1/models/{model}/sql", func(w http.ResponseWriter, r *http.Request) {
		handleQuery(svc, w, r, false)
	})
	return mux
}

func handleQuery(svc *serving.Service, w http.ResponseWriter, r *http.Request, execute bool) {
	m, err := svc.Resolve(r.PathValue("model"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req planner.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	id := governance.Identity{Role: r.Header.Get(RoleHeader)}
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

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var unknown serving.ErrUnknownModel
	switch {
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
