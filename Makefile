# Version from git tags (with fallback to "dev" if no tags exist)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Image URL to use all building/pushing image targets
IMG ?= ghcr.io/fredericrous/vault-transit-unseal-operator:$(VERSION)
# Kubernetes version for code generation
KUBE_VERSION ?= 1.29.0
# Get the currently used golang version
GO_VERSION := $(shell go version | awk '{print $$3}')

# ENVTEST binary versions
ENVTEST_K8S_VERSION = $(KUBE_VERSION)

# Get platform info
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'.
help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

# The CRD lives in three places and all three must agree. config/crd/bases is
# the controller-gen output and the source of truth; pkg/crd/crds is embedded
# in the binary and self-installed at startup; the chart's copy is what
# `helm install` applies. Filenames matter as much as contents — see
# pkg/crd/sync_test.go.
GENERATED_CRD_DIR ?= config/crd/bases
EMBEDDED_CRD_DIR  ?= pkg/crd/crds
CHART_CRD_DIR     ?= chart/vault-transit-unseal-operator/crds

manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=$(GENERATED_CRD_DIR)

sync-crds: manifests ## Copy the generated CRDs over the embedded and chart copies.
	@for dir in $(EMBEDDED_CRD_DIR) $(CHART_CRD_DIR); do \
		rm -f $$dir/*.yaml; \
		mkdir -p $$dir; \
		cp -v $(GENERATED_CRD_DIR)/*.yaml $$dir/; \
	done

verify-crds: ## Fail if the three CRD copies have drifted (contents or filenames).
	go test ./pkg/crd/... -run 'TestCRDCopiesAreIdentical|TestChartShipsExactlyTheGeneratedCRDs|TestEmbeddedCRDsAreNotDuplicated' -count=1

generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object paths="./..."

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

test: manifests generate fmt vet envtest ## Run tests.
	@echo "Setting up envtest binaries..."
	@test -d $(LOCALBIN) || mkdir -p $(LOCALBIN)
	@test -f $(ENVTEST) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./... -coverprofile cover.out

test-integration: manifests generate fmt vet envtest ## Run integration tests.
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./controllers -run Integration -v -ginkgo.v

test-unit: fmt vet ## Run unit tests only.
	go test ./api/... ./pkg/... -coverprofile cover.out

test-coverage: test ## Generate test coverage report.
	go tool cover -html=cover.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

test-coverage-business: ## Generate test coverage for business logic only (excluding infrastructure).
	@./scripts/coverage.sh

##@ Build

build: ## Build manager binary.
	go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} .
	docker tag ${IMG} ghcr.io/fredericrous/vault-transit-unseal-operator:latest

docker-push: ## Push docker image with the manager.
	docker push ${IMG}
	docker push ghcr.io/fredericrous/vault-transit-unseal-operator:latest

##@ Deployment

install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	kubectl apply -f manifests/core/vault-transit-unseal-operator/crds/

uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	kubectl delete -f manifests/core/vault-transit-unseal-operator/crds/

deploy: manifests ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	kubectl apply -k manifests/core/vault-transit-unseal-operator/

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	kubectl delete -k manifests/core/vault-transit-unseal-operator/

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.19.0

controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: all help manifests sync-crds verify-crds generate fmt vet test test-integration test-unit test-coverage build run docker-build docker-push install uninstall deploy undeploy controller-gen envtest