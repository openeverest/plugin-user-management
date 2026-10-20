IMG ?= ghcr.io/openeverest/plugin-user-management:dev
PLATFORM ?= linux/amd64

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

##@ Build

.PHONY: build-frontend
build-frontend: ## Build the frontend bundle into dist/main.js.
	npm run build

.PHONY: docker-build
docker-build: build-frontend ## Build the plugin image (IMG) for PLATFORM.
	docker buildx build --platform $(PLATFORM) -t $(IMG) .

.PHONY: docker-push
docker-push: build-frontend ## Build and push the plugin image (IMG) for PLATFORM.
	docker buildx build --platform $(PLATFORM) -t $(IMG) --push .

.PHONY: test
test: ## Run backend and frontend tests.
	cd backend && go test ./...
	npm test

CONTROLLER_GEN ?= $(shell go env GOPATH)/bin/controller-gen
$(CONTROLLER_GEN):
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate DeepCopy code and the DatabaseUser CRD from backend/api.
	cd backend && $(CONTROLLER_GEN) object:headerFile=../hack/boilerplate.go.txt paths=./api/...
	cd backend && $(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=../charts/plugin-user-management/crds

.PHONY: lint
lint: ## Vet the backend and lint the Helm chart.
	cd backend && go vet ./...
	helm lint charts/plugin-user-management

##@ Development on a remote cluster (current kubeconfig context)

DEV_TAG := dev-$(shell date +%s)
DEV_REGISTRY_LOCAL_PORT ?= 5005
DEV_IMAGE_TAR ?= /tmp/plugin-user-management-image.tar
CRANE ?= $(shell go env GOPATH)/bin/crane

$(CRANE):
	go install github.com/google/go-containerregistry/cmd/crane@latest

.PHONY: dev-registry
dev-registry: ## Deploy the throwaway in-cluster image registry (dev/registry.yaml).
	kubectl apply -f dev/registry.yaml
	kubectl -n dev-registry rollout status deploy/registry --timeout=120s
	kubectl -n dev-registry rollout status ds/registry-node-proxy --timeout=120s

.PHONY: dev-deploy
dev-deploy: build-frontend $(CRANE) ## Build the image, push it to the dev registry and helm upgrade the plugin.
	docker buildx build --platform $(PLATFORM) -t plugin-user-management:$(DEV_TAG) --output type=docker,dest=$(DEV_IMAGE_TAR) .
	kubectl -n dev-registry port-forward svc/registry $(DEV_REGISTRY_LOCAL_PORT):5000 >/dev/null & PF=$$!; \
		trap "kill $$PF" EXIT; sleep 3; \
		$(CRANE) push --insecure $(DEV_IMAGE_TAR) localhost:$(DEV_REGISTRY_LOCAL_PORT)/plugin-user-management:$(DEV_TAG)
	# Helm installs CRDs from crds/ but never upgrades them.
	kubectl apply --server-side -f charts/plugin-user-management/crds
	helm upgrade --install plugin-user-management charts/plugin-user-management -n everest-system \
		--set image.repository=localhost:5000/plugin-user-management --set image.tag=$(DEV_TAG) --wait
