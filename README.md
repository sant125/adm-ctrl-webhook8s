# DeployGuard Operator

Kubernetes Admission Webhook Operator que aplica políticas de deploy por namespace via CRD.

Um projeto-tutorial pra entender de ponta a ponta:
- como um Validating Admission Webhook funciona
- como o controller-runtime gerencia controllers, runnables e leader election
- como gerar e rotacionar TLS sem cert-manager
- como instrumentar com OpenTelemetry (traces + métricas) **vendor-neutral**
- práticas de hardening que caem em CKS

## O que faz

- **ValidatingWebhook**: rejeita Deployments que violam a `DeployPolicy` do namespace.
- **CRD `DeployPolicy`**: configuração declarativa por namespace, opt-in por label.
- **Cert Rotator próprio**: gera e renova o TLS do webhook (sem cert-manager).
- **OTEL nativo**: traces + métricas via OTLP, collector decide o destino.
- **Hardening por padrão**: PSS Restricted, seccomp, RBAC mínimo, NetworkPolicy.

## Estrutura do repositório

```
cmd/main.go                                  # entrypoint + wiring do manager
internal/
  controller/deploypolicy_controller.go      # reconcile loop do CRD
  webhook/
    server.go                                # HTTP TLS server (manager.Runnable)
    handler.go                               # roteamento /validate /healthz
    validate.go                              # lógica de validação + métricas + tracing
  policy/evaluator.go                        # orquestra as regras ativas
  rules/
    rule.go                                  # interface Rule + tipo Violation
    require_resources.go
    block_latest_tag.go
    require_readiness.go
  certrotator/rotator.go                     # geração e rotação de cert TLS
  telemetry/
    tracer.go                                # setup do TracerProvider OTEL
    meter.go                                 # setup do MeterProvider OTEL
  metrics/metrics.go                         # instrumentos OTEL globais
api/v1alpha1/deploypolicy_types.go           # tipos do CRD
config/
  crd/                                       # manifest do CRD
  webhook/                                   # ValidatingWebhookConfiguration
  rbac/                                      # SA, ClusterRole+Binding, Role+Binding, Service
  manager/                                   # Deployment do operator
  networkpolicy/                             # default-deny + allows mínimos
  otel-collector/                            # collector + Jaeger + ServiceMonitor
  monitoring/bootstrap.sh                    # instala kube-prometheus-stack + Tempo + Loki
  examples/demo.yaml                         # namespaces, policies e Deployments de exemplo
```

## Pré-requisitos

- Go 1.22+
- kubectl apontando pra um cluster Kubernetes 1.25+
- Container runtime (docker ou podman) com push acesso a um registry
- Helm (só pro `monitoring/bootstrap.sh`)

## Quick start: subir tudo num cluster novo

```bash
# 1. Stack de observabilidade (Prometheus + Grafana + Tempo + Loki)
./config/monitoring/bootstrap.sh

# 2. Collector OTEL + Jaeger (recebe OTLP do operator, distribui)
make deploy-otel

# 3. Build e push da imagem
make docker-build docker-push IMG=seu-registry/deployguard:v0.1.1

# 4. Operator no cluster (CRD, RBAC, webhook, deployment, NetworkPolicy)
make deploy IMG=seu-registry/deployguard:v0.1.1

# 5. Verificar saúde
kubectl -n deployguard-system get pods,svc,role,rolebinding,networkpolicy
kubectl get validatingwebhookconfigurations deployguard-validating-webhook \
  -o jsonpath='{.webhooks[0].clientConfig.caBundle}' | head -c 80   # deve estar preenchido pelo cert rotator

# 6. Aplicar exemplos (cria namespaces production/staging já com label opt-in,
#    DeployPolicies, e tenta criar um Deployment ruim que será REJEITADO)
kubectl apply -f config/examples/demo.yaml
```

Saída esperada do passo 6:
```
namespace/production created
namespace/staging created
deploypolicy.deployguard.io/production-policy created
deploypolicy.deployguard.io/staging-policy created
deployment.apps/good-deployment created
Error from server: error when creating "demo.yaml": admission webhook
"validate.deployguard.io" denied the request: deployment rejected by
DeployGuard (3 violation(s)):
  1. [spec.template.spec.containers[0].resources.requests] container "app" must have resources.requests defined
  2. [spec.template.spec.containers[0].image] container "app" uses image "nginx:latest" — tag ':latest' or untagged images are not allowed; ...
  3. [spec.template.spec.containers[0].readinessProbe] container "app" must have a readinessProbe defined ...
```

## Opt-in por namespace

O webhook só atua em namespaces com a label `deployguard.io/enforce=enabled`. Isso evita que um operator novo quebre coisa em namespace alheio.

```bash
# Habilitar
kubectl label namespace meu-app deployguard.io/enforce=enabled

# Desabilitar (rollback rápido)
kubectl label namespace meu-app deployguard.io/enforce-
```

`kube-system`, `kube-public`, `kube-node-lease` e `deployguard-system` ficam **bloqueados mesmo se labelados** (cinto-e-suspensório no `webhookconfig.yaml`).

## Observabilidade — onde olhar o que

Toda instrumentação é **vendor-neutral** via OpenTelemetry. O app só fala OTLP; o collector decide o destino.

| Sinal | Como o app emite | Onde aparece |
|---|---|---|
| **Traces** | `otel.Tracer("deployguard/...").Start(...)` | Tempo (Grafana → Explore → Tempo) ou Jaeger UI |
| **Métricas** | `otel.Meter("deployguard").Int64Counter(...)` | Prometheus → Grafana, via collector exporter prometheus :8889 |
| **Logs** | `log.FromContext(ctx).Info(...)` (zap) | Loki → Grafana → Explore |

### Métricas customizadas

Definidas em `internal/metrics/metrics.go`:

| Nome OTEL | Nome Prom | Tipo | Atributos | O que conta |
|---|---|---|---|---|
| `deployguard.admission.requests` | `deployguard_admission_requests_total` | Counter | namespace, operation, decision | toda admission processada |
| `deployguard.policy.violations` | `deployguard_policy_violations_total` | Counter | namespace, rule | cada violação encontrada (1+ por request) |
| `deployguard.admission.duration` | `deployguard_admission_duration_seconds` | Histogram | decision | latência por admission |

Queries PromQL úteis:

```promql
# Taxa de deny por minuto, por namespace
sum(rate(deployguard_admission_requests_total{decision="deny"}[5m])) by (namespace)

# Top 3 regras mais violadas na última semana
topk(3, sum by (rule) (rate(deployguard_policy_violations_total[7d])))

# p95 de latência do webhook (alerta se passar de 500ms — perto do timeout)
histogram_quantile(0.95,
  sum(rate(deployguard_admission_duration_seconds_bucket[5m])) by (le))
```

### Inspecionando manualmente

```bash
# Métricas internas do controller-runtime (formato Prom direto no operator)
kubectl -n deployguard-system port-forward deploy/deployguard-controller 8080:8080
curl localhost:8080/metrics | grep -E "controller_|workqueue_"

# Métricas customizadas (saem via OTLP → collector → endpoint Prom :8889)
kubectl -n observability port-forward svc/otel-collector 8889:8889
curl localhost:8889/metrics | grep deployguard_

# Traces no Jaeger
kubectl -n observability port-forward svc/jaeger 16686:16686
# abre http://localhost:16686 → service: deployguard

# Em DEV (ENV != production), traces e métricas saem no STDOUT do pod —
# útil pra validar instrumentação sem subir backend nenhum.
kubectl -n deployguard-system logs deploy/deployguard-controller -f
```

## Validando o hardening

```bash
# 1. seccomp ativo (deve mostrar Seccomp: 2)
kubectl -n deployguard-system exec deploy/deployguard-controller -- grep Seccomp /proc/1/status

# 2. RBAC mínimo: SA não consegue ler secret fora do próprio namespace
kubectl auth can-i get secrets \
  --as=system:serviceaccount:deployguard-system:deployguard-controller \
  -n kube-system
# → no

# 3. RBAC com resourceNames: só consegue ler O secret específico
kubectl auth can-i get secret/deployguard-webhook-cert \
  --as=system:serviceaccount:deployguard-system:deployguard-controller \
  -n deployguard-system
# → yes
kubectl auth can-i get secret/algum-outro \
  --as=system:serviceaccount:deployguard-system:deployguard-controller \
  -n deployguard-system
# → no

# 4. Cert tem serial aleatório (não "01")
kubectl -n deployguard-system get secret deployguard-webhook-cert \
  -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -serial

# 5. NetworkPolicy isolando o namespace
kubectl -n deployguard-system run test --rm -it --restart=Never \
  --image=busybox -- wget -T5 -qO- google.com
# → timeout (egress externo bloqueado, exceto 443)

kubectl -n deployguard-system run test --rm -it --restart=Never \
  --image=busybox -- wget -T5 -qO- otel-collector.observability.svc:4318
# → 405 Method Not Allowed (passou, é só GET num endpoint OTLP HTTP)

# 6. failurePolicy: Fail — derrubar o operator BLOQUEIA novos deploys
kubectl -n deployguard-system scale deploy/deployguard-controller --replicas=0
kubectl -n production apply -f config/examples/demo.yaml
# → error: failed calling webhook ... connection refused
kubectl -n deployguard-system scale deploy/deployguard-controller --replicas=1
```

## Adicionando uma nova regra

1. Criar `internal/rules/minha_regra.go` implementando `Rule`:
   ```go
   type MinhaRegra struct{}
   func (r *MinhaRegra) Name() string { return "minha-regra" }
   func (r *MinhaRegra) Evaluate(d *appsv1.Deployment) []Violation { ... }
   ```
2. Adicionar campo `boolean` em `DeployPolicySpec` em `api/v1alpha1/deploypolicy_types.go`
3. Adicionar campo correspondente no `config/crd/deploypolicy.yaml`
4. Registrar em `internal/policy/evaluator.go` dentro de `New()`
5. Escrever teste em `internal/rules/minha_regra_test.go`
6. `make test`

A métrica `deployguard_policy_violations_total{rule="minha-regra"}` aparece automaticamente — não precisa registrar nada.

## Conceitos-chave por arquivo

| Arquivo | Conceito Kubernetes / Go |
|---|---|
| `cmd/main.go` | manager.Runnable, leader election, graceful shutdown, ordem do setup OTEL |
| `internal/controller/deploypolicy_controller.go` | Reconcile loop, Finalizers, Status Conditions, idempotência |
| `internal/webhook/server.go` | `tls.Config.GetCertificate` para hot-reload de cert; manager.Runnable |
| `internal/webhook/validate.go` | AdmissionReview, AdmissionResponse, instrumentação OTEL com defer-closure |
| `internal/policy/evaluator.go` | Strategy pattern, span filhos por regra, métricas por violação |
| `internal/certrotator/rotator.go` | x509 self-signed, Strategic Merge Patch em WebhookConfiguration |
| `internal/telemetry/{tracer,meter}.go` | TracerProvider e MeterProvider, exporters OTLP vs stdout |
| `internal/metrics/metrics.go` | OTEL Meter API, cardinalidade, escolha de buckets |
| `config/rbac/rbac.yaml` | ClusterRole vs Role, resourceNames, separação por escopo |
| `config/networkpolicy/networkpolicy.yaml` | default-deny, allows por componente, limitações da CNI |
| `config/webhook/webhookconfig.yaml` | failurePolicy, namespaceSelector vs objectSelector, opt-in por label |
| `config/manager/deployment.yaml` | securityContext PSS Restricted, seccompProfile, probes |
| `config/otel-collector/collector.yaml` | receivers/processors/exporters, pipelines de traces e metrics |
| `Dockerfile` | multi-stage build, distroless nonroot, binário estático |

## Roadmap

Próximos passos planejados (ordem aproximada):

- [ ] **envtest**: testes de integração com API server local (sem precisar de cluster)
- [ ] **enforceMode**: `warn` vs `deny` no CRD — modo dry-run estilo Kyverno
- [ ] **Mutating webhook**: implementar `/mutate` com JSON Patch RFC 6902 (injetar resources defaults, labels)
- [ ] **PrometheusRule via reconcile**: o controller cria `PrometheusRule` por DeployPolicy, com OwnerReference (GC automático)
- [ ] **ValidatingAdmissionPolicy (CEL)**: implementar uma das regras em CEL nativo (K8s 1.30+) pra comparar tradeoffs
- [ ] **Helm chart**: substituir `envsubst` no `make deploy`
- [ ] **CI**: GitHub Actions com build, sign (cosign), SBOM, e2e em kind

## Como rodar localmente sem cluster (modo dev)

```bash
# Sem ENV=production, OTEL exporta pra stdout — você vê tudo no terminal
make run
```

O webhook em si não funciona em local (precisa cert TLS válido pra que o API server confie), mas o controller e o reconcile loop sim. Pra testar webhook sem cluster real, ver task **envtest** no roadmap.
