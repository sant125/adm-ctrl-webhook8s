package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/santzin/deployguard/api/v1alpha1"
	"github.com/santzin/deployguard/internal/metrics"
	"github.com/santzin/deployguard/internal/policy"
	"github.com/santzin/deployguard/internal/rules"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Valores de decision usados nas métricas. Constantes evitam typo em string.
const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
	decisionError = "error"
)

var tracer = otel.Tracer("deployguard/webhook")

// handleValidate processa requisições do ValidatingWebhook.
//
// FLUXO:
// 1. Lê o AdmissionReview do body
// 2. Deserializa o Deployment do campo "object"
// 3. Busca a DeployPolicy do namespace
// 4. Roda o Evaluator
// 5. Retorna allowed:true ou allowed:false com mensagem de erro
func (h *Handler) handleValidate(w http.ResponseWriter, r *http.Request) {
	// Span raiz desta request. Todos os spans abertos com este ctx
	// (policy.Evaluate, cache.List) viram filhos automaticamente.
	ctx, span := tracer.Start(r.Context(), "admission/validate")
	defer span.End()
	r = r.WithContext(ctx)

	// ==================================================================
	// MÉTRICAS OTEL — contabilizar toda admission, qualquer que seja o fim.
	//
	// Técnica: closure em variáveis locais que vão sendo preenchidas ao
	// longo da função, e um único defer que observa o resultado final.
	//
	// Por que assim em vez de incrementar em cada return?
	// - Centraliza a lógica (fácil adicionar nova métrica depois).
	// - Impossível esquecer de contar num caminho de retorno novo.
	// - Captura duração TOTAL, incluindo o writeAdmissionResponse final.
	// ==================================================================
	start := time.Now()
	decision := decisionError // default: se der panic/early return, registra erro
	namespace := ""
	operation := ""
	defer func() {
		attrs := metric.WithAttributes(
			attribute.String("namespace", namespace),
			attribute.String("operation", operation),
			attribute.String("decision", decision),
		)
		metrics.AdmissionRequests.Add(ctx, 1, attrs)
		// Duração: só leva o atributo "decision" — namespace/operation inflariam
		// cardinalidade do histograma (que já é caro por natureza).
		metrics.AdmissionDuration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributes(attribute.String("decision", decision)))
	}()

	logger := log.FromContext(r.Context()).WithName("validate")

	// Lemos o body inteiro antes de decodificar.
	// Isso evita problemas com streaming e permite logar o body em caso de erro.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "failed to read request body")
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// AdmissionReview é o envelope que o API server usa para todos os webhooks.
	// Contém: UID da requisição, tipo de operação, objeto novo e objeto antigo.
	var admissionReview admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReview); err != nil {
		logger.Error(err, "failed to decode AdmissionReview")
		http.Error(w, "invalid AdmissionReview", http.StatusBadRequest)
		return
	}

	req := admissionReview.Request
	// Preenche os labels das métricas agora que já temos a request.
	namespace = req.Namespace
	operation = string(req.Operation)

	logger = logger.WithValues(
		"uid", req.UID,
		"namespace", req.Namespace,
		"name", req.Name,
		"operation", req.Operation,
	)

	// Enriquece o span com metadados da request.
	// Você filtra no Jaeger por namespace="production" pra ver só requests de prod.
	span.SetAttributes(
		attribute.String("k8s.namespace", req.Namespace),
		attribute.String("k8s.deployment", req.Name),
		attribute.String("k8s.operation", string(req.Operation)),
	)

	// Deserializa o Deployment do campo "object" do AdmissionRequest.
	// req.Object.Raw é o JSON bruto do objeto que está sendo criado/atualizado.
	var deploy appsv1.Deployment
	if err := json.Unmarshal(req.Object.Raw, &deploy); err != nil {
		logger.Error(err, "failed to decode Deployment")
		decision = decisionDeny
		writeAdmissionResponse(w, denyResponse(req.UID, "failed to decode deployment object"))
		return
	}

	// Busca a DeployPolicy do namespace onde o Deployment está sendo criado.
	deployPolicy, err := h.fetchPolicy(r.Context(), req.Namespace)
	if err != nil {
		logger.Error(err, "failed to fetch DeployPolicy")
		// IMPORTANTE: Em caso de erro ao buscar a policy, APROVAMOS o deploy.
		// Isso é "fail open" — evita travar deploys por problema no próprio operator.
		// Em ambientes de alta segurança, pode-se mudar para "fail closed" (deny).
		// Nas métricas fica como "error" pra ser visível em alertas.
		decision = decisionError
		writeAdmissionResponse(w, allowResponse(req.UID))
		return
	}

	// Sem policy no namespace → nenhuma restrição aplicada.
	if deployPolicy == nil {
		logger.V(1).Info("no DeployPolicy found in namespace, allowing")
		decision = decisionAllow
		writeAdmissionResponse(w, allowResponse(req.UID))
		return
	}

	// Cria o evaluator com as regras habilitadas na policy e avalia o Deployment.
	evaluator := policy.New(deployPolicy)
	violations := evaluator.Evaluate(r.Context(), &deploy)

	if len(violations) == 0 {
		logger.Info("deployment passed all policy checks")
		decision = decisionAllow
		writeAdmissionResponse(w, allowResponse(req.UID))
		return
	}

	// Formata as violações em uma mensagem legível para o dev.
	// O dev vai ver essa mensagem no output do `kubectl apply`.
	message := formatViolations(violations)
	logger.Info("deployment rejected by policy", "violations", len(violations))
	decision = decisionDeny
	writeAdmissionResponse(w, denyResponse(req.UID, message))
}

// fetchPolicy busca a DeployPolicy do namespace.
// Retorna nil (sem erro) se não houver policy — isso é comportamento esperado.
func (h *Handler) fetchPolicy(ctx context.Context, namespace string) (*v1alpha1.DeployPolicy, error) {
	var policyList v1alpha1.DeployPolicyList

	if err := h.Client.List(ctx, &policyList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing DeployPolicies in namespace %q: %w", namespace, err)
	}

	if len(policyList.Items) == 0 {
		return nil, nil
	}

	// Convenção: se houver múltiplas policies no namespace, usamos a primeira.
	// Em produção você pode querer implementar merge ou rejeitar múltiplas.
	return &policyList.Items[0], nil
}

// formatViolations transforma a lista de violações em uma string legível.
// É o que o dev vai ver quando o deploy for rejeitado.
func formatViolations(violations []rules.Violation) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("deployment rejected by DeployGuard (%d violation(s)):\n", len(violations)))

	for i, v := range violations {
		sb.WriteString(fmt.Sprintf("  %d. [%s] %s\n", i+1, v.Field, v.Message))
	}

	return sb.String()
}

// allowResponse constrói uma AdmissionResponse de aprovação.
// O UID deve ser o mesmo da requisição — é assim que o API server correlaciona.
func allowResponse(uid types.UID) admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: &admissionv1.AdmissionResponse{
			UID:     uid,
			Allowed: true,
		},
	}
}

// denyResponse constrói uma AdmissionResponse de rejeição com mensagem de erro.
// O Status.Message é o que aparece no output do `kubectl apply`.
func denyResponse(uid types.UID, message string) admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: &admissionv1.AdmissionResponse{
			UID:     uid,
			Allowed: false,
			Result: &metav1.Status{
				Code:    http.StatusForbidden,
				Message: message,
			},
		},
	}
}
