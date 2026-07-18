# Build, test, and release targets. Registry and tags are variables; nothing
# is hardcoded to an account.

REGISTRY   ?= 000000000000.dkr.ecr.us-west-2.amazonaws.com
IMAGE_BASE ?= $(REGISTRY)/ossie-semantic-operator
TAG        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
AWS_REGION ?= us-west-2
PLATFORM   ?= linux/amd64

CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen

.PHONY: all
all: build

## Development

.PHONY: generate
generate: ## Regenerate deepcopy and CRD manifests after editing api/
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:dir=./charts/semantic-operator/crds

.PHONY: build
build: ## Compile all binaries into bin/
	mkdir -p bin
	go build -o bin/manager ./cmd/manager
	go build -o bin/server ./cmd/server
	go build -o bin/ossiectl ./cmd/ossiectl

.PHONY: test
test: ## Unit and smoke tests
	go vet ./...
	go test ./... -count=1

.PHONY: cover
cover:
	go test ./... -coverprofile=coverage.txt -covermode=atomic

## Images

.PHONY: ecr-login
ecr-login: ## Authenticate docker to ECR
	aws ecr get-login-password --region $(AWS_REGION) | docker login --username AWS --password-stdin $(REGISTRY)

.PHONY: ecr-create
ecr-create: ## Create the ECR repositories (idempotent)
	-aws ecr create-repository --region $(AWS_REGION) --repository-name ossie-semantic-operator/manager 2>/dev/null
	-aws ecr create-repository --region $(AWS_REGION) --repository-name ossie-semantic-operator/server 2>/dev/null

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
