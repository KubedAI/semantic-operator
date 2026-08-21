# Local SaaS accounts demo.
#
# A single-node kind cluster running the whole lakehouse + semantic stack:
# Garage (S3), Postgres, Polaris (Iceberg REST catalog), StarRocks, the
# semantic operator/server, and DataHub. Every kubectl/helm/kind call uses a
# LOCAL kubeconfig (.kube/config in this directory), never ~/.kube/config.
#
# Run the stages in order; each is small and independently verifiable.
#
#   Setup (needs network, run once):
#     make tools charts-vendor charts-images images-pull
#   Bring up the platform (offline):
#     make cluster-up images-load
#     make garage-up postgres-up
#     make polaris-up polaris-catalog
#     make starrocks-up starrocks-catalog
#     make data-load
#     make operator-build operator-up opa-up models-apply
#     make gateway-up keycloak-up
#     make datahub-up datahub-ingest datahub-enrich
#   Expose DataHub over MCP so your own agent can use it:
#     make datahub-mcp-build datahub-mcp-up
#   Tear down (host data/ is preserved):
#     make cluster-down
#
.DEFAULT_GOAL := help
SHELL := /usr/bin/env bash

LOCAL_DIR   := $(CURDIR)
DATA_DIR    := $(LOCAL_DIR)/data
DEPLOY_DIR  := $(LOCAL_DIR)/deploy
SCRIPTS     := $(LOCAL_DIR)/scripts
CLUSTER_NAME ?= account-demo-local
export KUBECONFIG := $(LOCAL_DIR)/.kube/config
# Prefer tools fetched by `make tools` into ./bin, then fall back to PATH.
export PATH := $(LOCAL_DIR)/bin:$(PATH)

-include $(DEPLOY_DIR)/versions.lock

.PHONY: help
help: ## Show targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  kubeconfig: $(KUBECONFIG)"

# =============================================================================
# One-time setup — needs network, run once. Charts are vendored to disk and
# images are pulled into the local Docker cache, so later cluster rebuilds
# reload them with 'kind load' instead of hitting a registry.
# =============================================================================
.PHONY: tools
tools: ## (network) Fetch pinned kind/kubectl/helm/uv into ./bin for this host OS+arch
	@bash $(SCRIPTS)/tools-install.sh

.PHONY: charts-vendor
charts-vendor: ## (network) Vendor pinned helm charts into deploy/*/charts
	@bash $(SCRIPTS)/charts-vendor.sh

.PHONY: charts-images
charts-images: ## (offline) Enumerate DataHub/prereq chart images into images.txt
	@bash $(SCRIPTS)/charts-images.sh

.PHONY: images-pull
images-pull: ## (network) Pull all pinned images into the local Docker cache
	@bash $(SCRIPTS)/images-pull.sh

# =============================================================================
# Cluster + images — create the kind cluster and load the pinned images from
# the local Docker cache into the node (no registry).
# =============================================================================
.PHONY: cluster-up
cluster-up: ## Create the single-node kind cluster (local kubeconfig)
	@mkdir -p "$(DATA_DIR)" "$(dir $(KUBECONFIG))"
	@sed 's|__LOCAL_DATA_DIR__|$(DATA_DIR)|g' $(DEPLOY_DIR)/kind/cluster.yaml.tmpl > $(DEPLOY_DIR)/kind/cluster.yaml
	@if kind get clusters 2>/dev/null | grep -qx "$(CLUSTER_NAME)"; then \
	  echo "cluster '$(CLUSTER_NAME)' already exists"; \
	else \
	  kind create cluster --name "$(CLUSTER_NAME)" --image "$(KIND_NODE_IMAGE)" \
	    --config $(DEPLOY_DIR)/kind/cluster.yaml --kubeconfig "$(KUBECONFIG)"; \
	fi
	@kubectl --kubeconfig "$(KUBECONFIG)" get nodes

.PHONY: images-load
images-load: ## Load the pinned images from the Docker cache into the kind node
	@bash $(SCRIPTS)/images-load.sh

# =============================================================================
# Object store + database — Garage (S3-compatible) holds the Iceberg data and
# StarRocks storage; Postgres backs the Polaris catalog and DataHub metadata.
# =============================================================================
.PHONY: postgres-up
postgres-up: ## Deploy shared Postgres (polaris + datahub databases)
	@bash $(SCRIPTS)/postgres-up.sh

.PHONY: garage-up
garage-up: ## Deploy single-node Garage + layout, buckets, key, credentials
	@bash $(SCRIPTS)/garage-up.sh

# =============================================================================
# Iceberg catalog — Polaris (Iceberg REST catalog) on Garage, backed by Postgres.
# =============================================================================
.PHONY: polaris-up
polaris-up: ## Bootstrap + deploy Polaris server (relational-jdbc on Postgres)
	@bash $(SCRIPTS)/polaris-up.sh

.PHONY: polaris-catalog
polaris-catalog: ## Create/ensure the Iceberg REST catalog 'account-demo' on Garage
	@bash $(SCRIPTS)/polaris-catalog.sh

# =============================================================================
# Query engine — StarRocks (shared-data, 1 FE + 1 CN) reading Iceberg via Polaris.
# =============================================================================
.PHONY: starrocks-up
starrocks-up: ## Install StarRocks (1 FE + 1 CN, shared-data on Garage)
	@bash $(SCRIPTS)/starrocks-up.sh

.PHONY: starrocks-catalog
starrocks-catalog: ## Create the Iceberg REST external catalog 'iceberg' -> Polaris
	@bash $(SCRIPTS)/starrocks-catalog.sh

# =============================================================================
# Demo data — generate + load the ~45k-row dataset into Iceberg via StarRocks.
# =============================================================================
.PHONY: data-load
data-load: ## Load the ~45k-row small dataset into iceberg (catalog-owned)
	@bash $(SCRIPTS)/data-load.sh

# =============================================================================
# Semantic layer — the operator + server + the local SemanticModels.
# =============================================================================
.PHONY: operator-build
operator-build: ## Build + kind-load the manager & server images (offline)
	@bash $(SCRIPTS)/operator-build.sh

.PHONY: operator-up
operator-up: ## Install semantic-operator + server (namespace semantic-system)
	@bash $(SCRIPTS)/operator-up.sh

.PHONY: opa-up
opa-up: ## Deploy OPA as the external first-gate authorizer (namespace account-demo)
	@bash $(SCRIPTS)/opa-up.sh

.PHONY: models-apply
models-apply: ## Apply the 3 local SemanticModels (catalog=iceberg)
	@bash $(SCRIPTS)/models-apply.sh

# =============================================================================
# Data catalog — DataHub v1.6.0 (single replica, shared Postgres) + ingest/enrich.
# =============================================================================
.PHONY: datahub-up
datahub-up: keycloak-up ## Install OIDC-enabled DataHub + prerequisites in ns datahub
	@bash $(SCRIPTS)/datahub-up.sh

.PHONY: datahub-ingest
datahub-ingest: ## Ingest Iceberg tables (Polaris account-demo) into DataHub via uv (client-side)
	@bash $(SCRIPTS)/datahub-ingest.sh

.PHONY: datahub-enrich
datahub-enrich: ## Enrich ingested datasets with semantic metadata via uv (client-side)
	@bash $(SCRIPTS)/datahub-enrich.sh

# =============================================================================
# HTTPS and identity — Caddy gateway first, then Keycloak behind it.
# =============================================================================
.PHONY: gateway-up keycloak-up
gateway-up: ## Deploy the local-CA HTTPS gateway and install kind split DNS
	@bash $(SCRIPTS)/gateway-up.sh

keycloak-up: gateway-up ## Provision Keycloak DB, realm, clients, roles, and demo credentials
	@bash $(SCRIPTS)/keycloak-up.sh

# =============================================================================
# DataHub MCP server — exposes DataHub search, metadata, lineage, and glossary
# over MCP so your own agent (Claude Code, Kiro CLI, or any MCP host) can use it
# alongside the Semantic Operator MCP. See the docs for client registration.
# =============================================================================
.PHONY: datahub-mcp-build
datahub-mcp-build: ## Build + kind-load the DataHub MCP server image (installs mcp-server-datahub from PyPI)
	@bash $(SCRIPTS)/datahub-mcp-build.sh

.PHONY: datahub-mcp-up
datahub-mcp-up: ## Deploy the in-cluster DataHub MCP server (namespace datahub)
	@bash $(SCRIPTS)/datahub-mcp-up.sh

# =============================================================================
# Teardown + utilities
# =============================================================================
.PHONY: cluster-down
cluster-down: ## Delete the kind cluster (data/ on host is preserved)
	@kind delete cluster --name "$(CLUSTER_NAME)" || true

.PHONY: kubeconfig
kubeconfig: ## Print the local kubeconfig path (export KUBECONFIG=...)
	@echo "$(KUBECONFIG)"
