# Build, test, and release targets. Registry and tags are variables; nothing
# is hardcoded to an account.

REGISTRY   ?= 000000000000.dkr.ecr.us-west-2.amazonaws.com
IMAGE_BASE ?= $(REGISTRY)/semantic-operator
TAG        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
AWS_REGION ?= us-west-2
PLATFORM   ?= linux/amd64

BIN_DIR ?= $(CURDIR)/bin
CONTROLLER_GEN := $(BIN_DIR)/controller-gen
GOLANGCI_LINT := $(BIN_DIR)/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.12.2

.PHONY: all
all: build

## Development

.PHONY: tools
tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) ## Install pinned developer tools into bin/

$(CONTROLLER_GEN): Makefile go.mod go.sum
	mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) go install sigs.k8s.io/controller-tools/cmd/controller-gen

$(GOLANGCI_LINT): Makefile
	mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

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
lint: $(GOLANGCI_LINT) ## Check Go formatting and static analysis
	$(GOLANGCI_LINT) run ./...

.PHONY: cover
cover:
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
docker-push:
	docker push $(IMAGE_BASE)/manager:$(TAG)
	docker push $(IMAGE_BASE)/server:$(TAG)

## Deployment

.PHONY: helm-lint
helm-lint:
	helm lint charts/semantic-operator

.PHONY: deploy
deploy: ## Install/upgrade the chart (override values on the command line)
	helm upgrade --install semantic-operator charts/semantic-operator \
	  --namespace semantic-system --create-namespace \
	  --set image.repository=$(IMAGE_BASE) --set image.tag=$(TAG)

## Demo and benchmark (see examples/starrocks/retail/README.md for required env)

.PHONY: demo-data
demo-data: ## Load the TPC-DS subset as Iceberg tables through StarRocks
	go run ./examples/starrocks/retail/data

.PHONY: demo-nl
demo-nl: ## Answer QUESTION both ways (raw text-to-SQL vs semantic layer)
	go run ./examples/starrocks/retail/nl -question "$(QUESTION)"

.PHONY: bench
bench: ## Run the accuracy benchmark and write the retail example RESULTS.md
	go run ./examples/starrocks/retail/bench/runner -out examples/starrocks/retail/bench/RESULTS.md

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'
