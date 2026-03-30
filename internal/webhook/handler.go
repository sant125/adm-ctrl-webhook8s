package webhook

import (
	"encoding/json"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Handler implementa http.Handler e roteia as requisições do API server.
//
// O API server sempre envia um AdmissionReview no body (JSON).
// Nós respondemos com outro AdmissionReview com o resultado.
type Handler struct {
	Client client.Client
}

// ServeHTTP é o entry point de todas as requisições HTTP.
// Roteamos por path — cada endpoint tem responsabilidade única.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := log.FromContext(r.Context()).WithName("webhook-handler")

	switch r.URL.Path {
	case "/validate":
		// Validating webhook: decide se o objeto é aceito ou rejeitado.
		// Chamado APÓS todos os mutating webhooks.
		logger.V(1).Info("handling validate request")
		h.handleValidate(w, r)

	case "/healthz":
		// Health check usado pelo Kubernetes para saber se o pod está pronto.
		// O readinessProbe do deployment do operator aponta para cá.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))

	default:
		http.NotFound(w, r)
	}
}

// writeAdmissionResponse serializa e escreve a resposta HTTP.
// Centralizar isso garante Content-Type correto e tratamento de erros consistente.
func writeAdmissionResponse(w http.ResponseWriter, response interface{}) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Se falhar ao escrever a resposta, logamos mas não há muito a fazer.
		// O API server vai receber uma resposta malformada e rejeitar o request.
		log.Log.Error(err, "failed to write admission response")
	}
}
