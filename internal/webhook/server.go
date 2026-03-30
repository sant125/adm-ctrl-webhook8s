// Package webhook implementa o servidor HTTP TLS que recebe chamadas do API server.
//
// FLUXO DO ADMISSION WEBHOOK:
// 1. Dev faz `kubectl apply -f deployment.yaml`
// 2. API server recebe a requisição
// 3. API server chama TODOS os MutatingWebhooks registrados (em paralelo ou sequencial)
// 4. API server aplica as mutações retornadas
// 5. API server chama TODOS os ValidatingWebhooks registrados
// 6. Se qualquer validating webhook rejeitar → 403 para o dev
// 7. Se todos aprovarem → objeto é persistido no etcd
package webhook

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Server é o servidor HTTP TLS do webhook.
// Implementa manager.Runnable — o manager do controller-runtime chama Start(ctx).
type Server struct {
	// Client é o client do Kubernetes para buscar DeployPolicy nos namespaces.
	Client client.Client

	// Port é a porta de escuta. Padrão: 9443 (convenção do controller-runtime).
	Port int

	// CertDir é o diretório onde o cert rotator salva tls.crt e tls.key.
	CertDir string
}

// Start inicia o servidor HTTP e bloqueia até ctx ser cancelado.
// É chamado pelo manager em uma goroutine separada.
func (s *Server) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("webhook-server")

	// Handler agrega /validate, /mutate e /healthz
	handler := &Handler{
		Client: s.Client,
	}

	// tls.Config com GetCertificate permite hot-reload do certificado.
	// Em vez de carregar o cert uma vez no startup, lemos do disco a cada nova conexão TLS.
	// Isso é fundamental para o cert rotator funcionar sem restart do pod.
	tlsConfig := &tls.Config{
		GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(
				fmt.Sprintf("%s/tls.crt", s.CertDir),
				fmt.Sprintf("%s/tls.key", s.CertDir),
			)
			if err != nil {
				return nil, fmt.Errorf("loading webhook cert: %w", err)
			}
			return &cert, nil
		},
		MinVersion: tls.VersionTLS13, // TLS 1.3 mínimo — boas práticas de segurança
	}

	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", s.Port),
		Handler:   handler,
		TLSConfig: tlsConfig,

		// Timeouts são obrigatórios em servidores HTTP de produção.
		// O API server tem timeout de 10s para webhooks — nosso ReadTimeout deve ser menor.
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	// Goroutine para shutdown graceful quando ctx for cancelado (SIGTERM do Kubernetes).
	go func() {
		<-ctx.Done()
		log.Info("shutting down webhook server")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error(err, "webhook server shutdown error")
		}
	}()

	log.Info("starting webhook server", "port", s.Port)

	// ListenAndServeTLS com strings vazias usa o TLSConfig.GetCertificate acima.
	if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("webhook server error: %w", err)
	}

	return nil
}
