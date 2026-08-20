# Build, test, and release targets. Registry and tags are variables; nothing
# is hardcoded to an account.

REGISTRY   ?= 000000000000.dkr.ecr.us-west-2.amazonaws.com
IMAGE_BASE ?= $(REGISTRY)/semantic-operator
TAG        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
AWS_REGION ?= us-west-2
# Default to the host architecture so locally built images run on the machine
# that built them; kind runs its node on the host arch. uname -m reports
# x86_64 on Intel/AMD and aarch64 or arm64 on ARM. Override for cross-builds.
PLATFORM   ?= linux/$(if $(filter aarch64 arm64,$(shell uname -m)),arm64,amd64)

# Local kind commands share one repository-local kubeconfig and always select
# the named kind context explicitly.
KIND_CLUSTER_NAME ?= semantic-operator-dev
KIND_KUBECONFIG   ?= $(CURDIR)/.kube/config
KIND_IMAGE_BASE   ?= semantic-operator-kind
KIND_IMAGE_TAG    ?= local
KIND_NAMESPACE    ?= semantic-system
KIND_ENGINE_TYPE  ?= trino # or starrocks
AUTH_IDENTITY_MODE ?= static
KIND_TRINO_LOCAL_PORT ?= 18080
KIND_STARROCKS_LOCAL_PORT ?= 19030
KIND_NODE_IMAGE       ?=
KIND_RELEASE_NAME     ?= semantic-operator
KIND_RANGER_ADMIN_IMAGE      ?= apache/ranger:2.9.0
KIND_RANGER_DB_IMAGE         ?= postgres:16
KIND_RANGER_PDP_IMAGE        ?= semantic-ranger-pdp:2.9.0
KIND_RANGER_ADMIN_LOCAL_PORT ?= 16080
# Pin by digest (repo:tag@sha256:...) for an immutable engine image.
KIND_STARROCKS_IMAGE         ?= starrocks/allin1-ubuntu:4.0.13
STARROCKS_MANAGER_USER       ?= semantic-manager
STARROCKS_MANAGER_PASSWORD   ?= manager
STARROCKS_SERVER_USER        ?= semantic-server
STARROCKS_SERVER_PASSWORD    ?= server
STARROCKS_JWT_AUDIENCE       ?= starrocks

BIN_DIR ?= $(CURDIR)/bin
CONTROLLER_GEN := $(BIN_DIR)/controller-gen
GOLANGCI_LINT := $(BIN_DIR)/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.12.2
HELM := $(BIN_DIR)/helm
HELM_VERSION ?= v4.2.4
HELM_OS ?= $(shell uname -s | tr '[:upper:]' '[:lower:]')
HELM_ARCH ?= $(if $(filter aarch64 arm64,$(shell uname -m)),arm64,amd64)
HELM_UNITTEST_VERSION ?= v1.1.2

# Running make with no target prints the available targets rather than building.
.DEFAULT_GOAL := help

.PHONY: all
all: build

## Development

.PHONY: tools
tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) $(HELM) ## Install pinned developer tools into bin/

$(CONTROLLER_GEN): Makefile go.mod go.sum
	mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) go install sigs.k8s.io/controller-tools/cmd/controller-gen

$(GOLANGCI_LINT): Makefile
	mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Downloaded once into bin/ (gitignored), pinned by HELM_VERSION.
$(HELM):
	mkdir -p $(BIN_DIR)
	curl -fsSL https://get.helm.sh/helm-$(HELM_VERSION)-$(HELM_OS)-$(HELM_ARCH).tar.gz \
	  | tar -xzf - -C $(BIN_DIR) --strip-components=1 $(HELM_OS)-$(HELM_ARCH)/helm
	@$(HELM) version --short

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Regenerate deepcopy and CRD manifests after editing api/
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:dir=./charts/semantic-operator/crds

.PHONY: build
build: ## Compile all binaries into bin/
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/manager ./cmd/manager
	go build -o $(BIN_DIR)/server ./cmd/server
	go build -o $(BIN_DIR)/ossiectl ./cmd/ossiectl

.PHONY: test
test: ## Unit and smoke tests
	go vet ./...
	go test ./... -count=1

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Check Go formatting, static analysis, and Service exposure
	$(GOLANGCI_LINT) run ./...

.PHONY: cover
cover: ## Write a Go coverage profile to coverage.txt
	go test ./... -coverprofile=coverage.txt -covermode=atomic

## Images

.PHONY: ecr-login
ecr-login: ## Authenticate docker to ECR
	aws ecr get-login-password --region $(AWS_REGION) | docker login --username AWS --password-stdin $(REGISTRY)

.PHONY: ecr-create
ecr-create: ## Create the ECR repositories (idempotent)
	-aws ecr create-repository --region $(AWS_REGION) --repository-name semantic-operator/manager 2>/dev/null
	-aws ecr create-repository --region $(AWS_REGION) --repository-name semantic-operator/server 2>/dev/null

.PHONY: docker-build
docker-build: ## Build manager and server images
	docker build --platform $(PLATFORM) --target manager -t $(IMAGE_BASE)/manager:$(TAG) .
	docker build --platform $(PLATFORM) --target server  -t $(IMAGE_BASE)/server:$(TAG) .

.PHONY: docker-push
docker-push: ## Push manager and server images to the registry
	docker push $(IMAGE_BASE)/manager:$(TAG)
	docker push $(IMAGE_BASE)/server:$(TAG)

## Documentation site

.PHONY: docs
docs: docs-install ## Serve the docs site locally at http://localhost:4321
	cd website && npm run dev

.PHONY: docs-install
docs-install:
	cd website && npm install

.PHONY: docs-build
docs-build: docs-install ## Production build into website/dist
	cd website && npm run build

.PHONY: docs-check
docs-check: docs-build ## Build the site and fail on any broken internal link
	cd website && node scripts/check-links.mjs

.PHONY: docs-check-external
docs-check-external: docs-build ## Also probe outbound links (needs network)
	cd website && node scripts/check-links.mjs --external

## Deployment

.PHONY: kind-deploy
kind-deploy: ## Create or reuse the kind cluster and write .kube/config
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" \
	./hack/kind-deploy.sh

.PHONY: keycloak-deploy
keycloak-deploy: kind-deploy ## Deploy Keycloak and import the semantic realm
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	./hack/keycloak-deploy.sh

.PHONY: trino-secrets
trino-secrets: kind-deploy ## Create the Trino TLS keystore, password file, and operator engine credential Secrets
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	./hack/trino-secrets.sh

.PHONY: trino-deploy
trino-deploy: trino-secrets keycloak-deploy ## Deploy the single TLS+auth Trino engine to the kind cluster
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	./hack/trino-deploy.sh

.PHONY: operator-deploy
operator-deploy: $(if $(filter trino,$(KIND_ENGINE_TYPE)),trino-deploy,$(if $(filter starrocks,$(KIND_ENGINE_TYPE)),starrocks-deploy,kind-deploy)) ## Build, load into kind, and install the semantic operator and server
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_IMAGE_BASE="$(KIND_IMAGE_BASE)" \
	KIND_IMAGE_TAG="$(KIND_IMAGE_TAG)" \
	KIND_PLATFORM="$(PLATFORM)" \
	KIND_ENGINE_TYPE="$(KIND_ENGINE_TYPE)" \
	KIND_RELEASE_NAME="$(KIND_RELEASE_NAME)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	./hack/operator-deploy.sh

.PHONY: auth-operator
auth-operator: trino-deploy ## Deploy the operator in AUTH_IDENTITY_MODE (passthrough|static|exchange) and publish the identity model
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_IMAGE_BASE="$(KIND_IMAGE_BASE)" \
	KIND_IMAGE_TAG="$(KIND_IMAGE_TAG)" \
	KIND_PLATFORM="$(PLATFORM)" \
	KIND_RELEASE_NAME="$(KIND_RELEASE_NAME)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	AUTH_IDENTITY_MODE="$(AUTH_IDENTITY_MODE)" \
	./hack/auth-operator.sh

.PHONY: auth-e2e
auth-e2e: trino-deploy ## Deploy all three identity modes as parallel releases (sem-static/passthrough/exchange) for the Go e2e
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_IMAGE_BASE="$(KIND_IMAGE_BASE)" \
	KIND_IMAGE_TAG="$(KIND_IMAGE_TAG)" \
	KIND_PLATFORM="$(PLATFORM)" \
	KIND_RELEASE_NAME="$(KIND_RELEASE_NAME)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	./hack/auth-e2e.sh

.PHONY: starrocks-deploy
starrocks-deploy: kind-deploy keycloak-deploy ## Deploy a single-node StarRocks (allin1, shared-nothing) to kind with static + OIDC/JWT auth
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	KIND_STARROCKS_IMAGE="$(KIND_STARROCKS_IMAGE)" \
	STARROCKS_MANAGER_USER="$(STARROCKS_MANAGER_USER)" \
	STARROCKS_MANAGER_PASSWORD="$(STARROCKS_MANAGER_PASSWORD)" \
	STARROCKS_SERVER_USER="$(STARROCKS_SERVER_USER)" \
	STARROCKS_SERVER_PASSWORD="$(STARROCKS_SERVER_PASSWORD)" \
	STARROCKS_JWT_AUDIENCE="$(STARROCKS_JWT_AUDIENCE)" \
	./hack/starrocks-deploy.sh

.PHONY: ranger-deploy
ranger-deploy: kind-deploy ## Deploy a minimal Apache Ranger Admin and PDP stack to kind
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	KIND_RANGER_ADMIN_IMAGE="$(KIND_RANGER_ADMIN_IMAGE)" \
	KIND_RANGER_DB_IMAGE="$(KIND_RANGER_DB_IMAGE)" \
	KIND_RANGER_PDP_IMAGE="$(KIND_RANGER_PDP_IMAGE)" \
	KIND_RANGER_PDP_PLATFORM="$(PLATFORM)" \
	KIND_RANGER_ADMIN_LOCAL_PORT="$(KIND_RANGER_ADMIN_LOCAL_PORT)" \
	./hack/ranger-deploy.sh

.PHONY: opa-deploy
opa-deploy: operator-deploy ## Deploy the pinned OPA image and retail example policy to kind
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	./hack/opa-deploy.sh

.PHONY: models-deploy
models-deploy: $(if $(filter trino,$(KIND_ENGINE_TYPE)),opa-deploy,operator-deploy) ## Load retail data and publish the E2E model for KIND_ENGINE_TYPE (trino or starrocks)
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	KIND_ENGINE_TYPE="$(KIND_ENGINE_TYPE)" \
	KIND_TRINO_LOCAL_PORT="$(KIND_TRINO_LOCAL_PORT)" \
	KIND_STARROCKS_LOCAL_PORT="$(KIND_STARROCKS_LOCAL_PORT)" \
	./hack/models-deploy.sh

.PHONY: e2e
e2e: models-deploy ## Prepare kind, Trino, operator, OPA, data, and model for manual tests
	@echo "E2E environment is ready"

.PHONY: quickstart
quickstart: kind-deploy ## Minimal local stack for the Quickstart: plaintext Trino, operator, demo data, plain model (no auth, no OPA/Ranger)
	KIND_CLUSTER_NAME="$(KIND_CLUSTER_NAME)" \
	KIND_KUBECONFIG="$(KIND_KUBECONFIG)" \
	KIND_IMAGE_BASE="$(KIND_IMAGE_BASE)" \
	KIND_IMAGE_TAG="$(KIND_IMAGE_TAG)" \
	KIND_PLATFORM="$(PLATFORM)" \
	KIND_RELEASE_NAME="$(KIND_RELEASE_NAME)" \
	KIND_NAMESPACE="$(KIND_NAMESPACE)" \
	KIND_TRINO_LOCAL_PORT="$(KIND_TRINO_LOCAL_PORT)" \
	./hack/quickstart.sh

.PHONY: helm-lint
helm-lint: $(HELM) ## Lint the Helm chart against a valid configuration
	# Lint a valid configuration: with default values the chart's guardrails
	# (engine.host required, header auth must be acknowledged) fire during
	# rendering and drown the output in expected failures.
	$(HELM) lint charts/semantic-operator \
	  --set engine.host=lint.example \
	  --set server.auth.allowInsecureHeaderAuth=true

.PHONY: helm-unittest
helm-unittest: export HELM_DATA_HOME := $(BIN_DIR)/.helm-data
helm-unittest: $(HELM) ## Run the Helm chart unit tests (installs the pinned helm-unittest plugin under bin/ if missing)
	@$(HELM) plugin list 2>/dev/null | grep -q '^unittest' || \
	  $(HELM) plugin install https://github.com/helm-unittest/helm-unittest.git --version $(HELM_UNITTEST_VERSION) --verify=false
	$(HELM) unittest charts/semantic-operator

## Demo and benchmark (see examples/retail/README.md for required env)

.PHONY: demo-data
demo-data: ## Load the TPC-DS subset as Iceberg tables through StarRocks
	go run ./examples/retail/data

.PHONY: demo-nl
demo-nl: ## Answer QUESTION both ways (raw text-to-SQL vs semantic layer)
	go run ./examples/retail/nl -question "$(QUESTION)"

.PHONY: bench
bench: ## Run the accuracy benchmark and write the retail example RESULTS.md
	go run ./examples/retail/bench/runner -out examples/retail/bench/RESULTS.md

.PHONY: help
help:
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'
