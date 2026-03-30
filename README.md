# DeployGuard Operator

Kubernetes Admission Webhook Operator que aplica políticas de deploy por namespace via CRD.

## O que faz

- **ValidatingWebhook**: rejeita Deployments que violam a `DeployPolicy` do namespace
- **MutatingWebhook**: injeta defaults (resources, labels) antes da validação
- **CRD `DeployPolicy`**: configuração declarativa por namespace
- **Cert Rotator**: gera e renova o TLS do webhook automaticamente (sem cert-manager)

## Estrutura

```
cmd/main.go                          # entrypoint + wiring do manager
internal/
  controller/deploypolicy_controller.go  # reconcile loop do CRD
  webhook/
    server.go                        # HTTP TLS server
    handler.go                       # roteamento /validate /mutate /healthz
    validate.go                      # lógica de validação
    mutate.go                        # lógica de mutação (JSON Patch)
  policy/evaluator.go                # orquestra as regras ativas
  rules/
    rule.go                          # interface Rule + tipo Violation
    require_resources.go
    block_latest_tag.go
    require_readiness.go
  certrotator/rotator.go             # geração e rotação de cert TLS
api/v1alpha1/deploypolicy_types.go   # tipos do CRD
config/
  crd/                               # manifest do CRD
  webhook/                           # ValidatingWebhookConfiguration + Mutating
  rbac/                              # ServiceAccount, ClusterRole, Service
  examples/demo.yaml                 # exemplos de DeployPolicy + Deployments
```

## Pré-requisitos

- Go 1.22+
- kubectl configurado para um cluster Kubernetes 1.25+
- Docker (para build da imagem)

## Desenvolvimento local

```bash
# Instalar ferramentas
make controller-gen

# Gerar deepcopy e manifests CRD
make generate
make manifests

# Rodar testes
make test

# Build do binário
make build
```

## Deploy no cluster

```bash
# 1. Build e push da imagem
make docker-build docker-push IMG=seu-registry/deployguard:v0.1.0

# 2. Instalar CRD
make install

# 3. Deploy completo
make deploy IMG=seu-registry/deployguard:v0.1.0

# Verificar se o operator está rodando
kubectl get pods -n deployguard-system
kubectl get deploypolicies -A
```

## Testando o webhook

```bash
# Criar namespaces de teste
kubectl create namespace production
kubectl create namespace staging

# Aplicar as políticas de exemplo
kubectl apply -f config/examples/demo.yaml

# Tentar criar o deployment ruim — deve ser rejeitado
kubectl apply -f config/examples/demo.yaml  # bad-deployment
# Saída esperada: Error from server: admission webhook denied the request

# Criar o deployment bom — deve ser aceito
# (o mutating webhook vai adicionar os labels managed-by e env)
kubectl get deployment good-deployment -n production -o jsonpath='{.spec.template.metadata.labels}'
```

## Adicionando uma nova regra

1. Crie `internal/rules/minha_regra.go` implementando a interface `Rule`
2. Adicione um campo no `DeployPolicySpec` em `api/v1alpha1/deploypolicy_types.go`
3. Registre a regra no `policy/evaluator.go` dentro de `New()`
4. Rode `make manifests` para regenerar o CRD
5. Escreva o teste em `internal/rules/minha_regra_test.go`

## Conceitos-chave aprendidos

| Componente | Conceito Kubernetes |
|---|---|
| `certrotator` | TLS bootstrapping, patch em WebhookConfiguration |
| `webhook/server.go` | `tls.Config.GetCertificate` para hot-reload de cert |
| `webhook/validate.go` | AdmissionReview, AdmissionResponse, failurePolicy |
| `webhook/mutate.go` | JSON Patch RFC 6902, idempotência, reinvocationPolicy |
| `controller` | Reconcile loop, Finalizers, Status Conditions |
| `main.go` | manager.Runnable, leader election, graceful shutdown |
