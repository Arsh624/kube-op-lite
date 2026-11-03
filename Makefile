# ====================================================================================
# Kube-Op-Lite Makefile
# Run these from a Linux/macOS shell (on Windows use WSL2). See README for setup.
# ====================================================================================

IMG ?= kube-op-lite:dev
KIND_CLUSTER ?= kube-op-lite
RELEASE ?= kube-op-lite
NAMESPACE ?= kube-op-lite-system

# Tool versions pinned via `go run` so you don't need them globally installed.
CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.15.0
ENVTEST ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.18
ENVTEST_K8S_VERSION ?= 1.30.0

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

##@ Development

.PHONY: manifests
manifests: ## Regenerate CRD, RBAC and webhook manifests from code markers.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook \
		paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: ## Regenerate DeepCopy methods (zz_generated.deepcopy.go).
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet ## Run unit tests (envtest spins up a local API server).
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: fmt vet ## Build the manager binary into bin/.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run the controller locally against your current kube context.
	ENABLE_WEBHOOKS=false go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build the operator container image.
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push the operator container image.
	docker push $(IMG)

##@ Local cluster (Kind)

.PHONY: kind-create
kind-create: ## Create a local Kind cluster.
	kind create cluster --name $(KIND_CLUSTER)

.PHONY: kind-delete
kind-delete: ## Delete the local Kind cluster.
	kind delete cluster --name $(KIND_CLUSTER)

.PHONY: kind-load
kind-load: docker-build ## Build and load the image into Kind (no registry needed).
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)

##@ Deploy (Helm)

.PHONY: helm-install
helm-install: ## Install/upgrade the operator via the Helm chart.
	helm upgrade --install $(RELEASE) charts/kube-op-lite \
		--namespace $(NAMESPACE) --create-namespace \
		--set image.repository=$(word 1,$(subst :, ,$(IMG))) \
		--set image.tag=$(word 2,$(subst :, ,$(IMG))) \
		--wait --timeout 3m

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the operator.
	helm uninstall $(RELEASE) --namespace $(NAMESPACE)

.PHONY: sample
sample: ## Apply the example GpuWorkload.
	kubectl apply -f config/samples/gpu_v1alpha1_gpuworkload.yaml

.PHONY: kind-gpus
kind-gpus: ## Advertise fake nvidia.com/gpu capacity on Kind nodes (GPUS=4 per node).
	bash hack/kind-fake-gpus.sh $(or $(GPUS),4)

.PHONY: cert-manager
cert-manager: ## Install cert-manager (issues the webhook TLS cert).
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml
	kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=180s

.PHONY: demo
demo: kind-create kind-gpus cert-manager kind-load helm-install sample ## One-shot: cluster + fake GPUs + operator + sample.
	@echo ">> Watch it converge:  kubectl get gpuworkloads -w"

.PHONY: preemption-demo
preemption-demo: ## Show a high-priority workload preempting a low-priority one for GPUs.
	bash hack/preemption-demo.sh
