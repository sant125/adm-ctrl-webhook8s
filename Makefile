# ============================================================
# Makefile do DeployGuard Operator
# Convenção do kubebuilder — targets padrão que qualquer
# engenheiro de platform vai reconhecer.
# ============================================================

# Variáveis configuráveis via env ou linha de comando.
# Exemplo: make docker-build IMG=myregistry/deployguard:v0.2.0
IMG          ?= deployguard:latest
NAMESPACE    ?= deployguard-system

# Detecta o binário de container (docker ou podman).
CONTAINER_TOOL ?= docker

# Versão do controller-gen para gerar CRD e deepcopy.
CONTROLLER_GEN_VERSION ?= v0.14.0

# Diretório dos binários de ferramentas locais (não polui o PATH global).
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Caminhos dos binários de ferramentas.
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

# ============================================================
# TARGETS PRINCIPAIS
# ============================================================

.PHONY: all
all: build ## Build padrão

.PHONY: help
help: ## Mostra esta ajuda
	@awk 'BEGIN {FS = ":.*##"; printf "\nUso:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ============================================================
# DESENVOLVIMENTO
# ============================================================

.PHONY: fmt
fmt: ## Formata o código Go
	go fmt ./...

.PHONY: vet
vet: ## Roda go vet (análise estática básica)
	go vet ./...

.PHONY: lint
lint: ## Roda golangci-lint (precisa estar instalado)
	golangci-lint run ./...

.PHONY: test
test: ## Roda os testes unitários
	go test ./... -v -count=1

.PHONY: test-cover
test-cover: ## Roda testes com cobertura e abre o relatório HTML
	go test ./... -coverprofile=cover.out
	go tool cover -html=cover.out

.PHONY: build
build: fmt vet ## Compila o binário local (para desenvolvimento)
	go build -o bin/manager ./cmd/main.go

.PHONY: run
run: ## Roda o operator localmente (usa o kubeconfig atual)
	# KUBECONFIG precisa apontar para um cluster com o CRD instalado.
	# O webhook NÃO funciona localmente sem TLS válido — use só o controller.
	go run ./cmd/main.go

# ============================================================
# GERAÇÃO DE CÓDIGO
# ============================================================

.PHONY: generate
generate: controller-gen ## Gera o código deepcopy (zz_generated.deepcopy.go)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: controller-gen ## Gera os manifests CRD a partir das annotations +kubebuilder
	$(CONTROLLER_GEN) rbac:roleName=deployguard-controller crd webhook paths="./..." output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac

# ============================================================
# DOCKER / OCI
# ============================================================

.PHONY: docker-build
docker-build: ## Build da imagem Docker
	$(CONTAINER_TOOL) build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push da imagem para o registry
	$(CONTAINER_TOOL) push $(IMG)

.PHONY: docker-build-push
docker-build-push: docker-build docker-push ## Build + push em sequência

# ============================================================
# DEPLOY NO CLUSTER
# ============================================================

.PHONY: install
install: ## Instala o CRD no cluster (kubectl apply)
	kubectl apply -f config/crd/

.PHONY: uninstall
uninstall: ## Remove o CRD do cluster
	kubectl delete -f config/crd/ --ignore-not-found

.PHONY: deploy
deploy: ## Deploy completo: namespace + rbac + webhook config + operator
	kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f config/crd/
	kubectl apply -f config/rbac/
	kubectl apply -f config/webhook/
	# Substitui a imagem no deployment antes de aplicar.
	# Em produção use kustomize ou helm para isso.
	IMG=$(IMG) envsubst < config/manager/deployment.yaml | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Remove tudo do cluster
	kubectl delete -f config/webhook/ --ignore-not-found
	kubectl delete -f config/rbac/ --ignore-not-found
	kubectl delete -f config/crd/ --ignore-not-found
	kubectl delete namespace $(NAMESPACE) --ignore-not-found

# ============================================================
# INSTALAÇÃO DE FERRAMENTAS
# ============================================================

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Garante que controller-gen está instalado
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_GEN_VERSION))

# Função auxiliar para instalar ferramentas Go no LOCALBIN.
define go-install-tool
@[ -f $(1) ] || { \
	set -e ;\
	echo "Installing $(2)@$(3)" ;\
	GOBIN=$(LOCALBIN) go install $(2)@$(3) ;\
}
endef
