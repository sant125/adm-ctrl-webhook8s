// main.go é o entrypoint do operator.
// Responsabilidade: wiring — conectar todos os componentes e iniciar o manager.
//
// ARQUITETURA GERAL:
//
//	Manager (controller-runtime)
//	├── DeployPolicyReconciler  (goroutine — reconcile loop)
//	├── CertRotator             (goroutine — renova cert TLS a cada 24h)
//	└── WebhookServer           (goroutine — HTTP TLS server)
//
// O Manager coordena o lifecycle de tudo:
// - Aguarda o cache sincronizar antes de iniciar os controllers
// - Propaga o ctx cancelado (SIGTERM) para todos os Runnables
// - Gerencia leader election se configurado
package main

import (
	"context"
	"os"

	"github.com/santzin/deployguard/api/v1alpha1"
	"github.com/santzin/deployguard/internal/certrotator"
	"github.com/santzin/deployguard/internal/controller"
	"github.com/santzin/deployguard/internal/telemetry"
	"github.com/santzin/deployguard/internal/webhook"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// No controller-runtime v0.17 o metrics server foi extraído num pacote próprio.
	// MetricsBindAddress (field antigo) virou Metrics.BindAddress dentro de Options.
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	// scheme é o registro de tipos do Kubernetes.
	// Precisamos registrar todos os tipos que vamos usar com o client.
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// Registra os tipos built-in do Kubernetes (Pod, Deployment, etc).
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// Registra os tipos built-in que usamos explicitamente.
	utilruntime.Must(appsv1.AddToScheme(scheme))

	// Registra nossos tipos customizados (DeployPolicy).
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	// Configura o logger estruturado (JSON em prod, texto legível em dev).
	// zap é o logger padrão do controller-runtime.
	ctrl.SetLogger(zap.New(zap.UseDevMode(isDev())))

	// OTEL — mesmo padrão do SetLogger: registra global antes de subir qualquer componente.
	// Em dev imprime spans no stdout. Em prod exporta via OTLP pro collector.
	shutdownTracing, err := telemetry.Setup(context.Background())
	if err != nil {
		setupLog.Error(err, "unable to setup tracing")
		os.Exit(1)
	}
	defer shutdownTracing() // flush dos spans pendentes no graceful shutdown

	// Lê configurações do ambiente.
	// Preferimos env vars a flags para configuração de operator — mais Kubernetes-native.
	namespace := getEnvOrDefault("OPERATOR_NAMESPACE", "deployguard-system")
	certDir := getEnvOrDefault("CERT_DIR", "/tmp/deployguard-certs")
	webhookPort := 9443

	// MANAGER: o coração do operator.
	// Gerencia o lifecycle de controllers, webhooks e qualquer Runnable.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,

		// Porta do metrics server (Prometheus scrape).
		// v0.17+: metrics foi extraído para metricsserver.Options.
		Metrics: metricsserver.Options{BindAddress: ":8080"},

		// Porta do health/readiness probe.
		HealthProbeBindAddress: ":8081",

		// LeaderElection garante que apenas uma replica do operator processa eventos.
		// IMPORTANTE em deployments com múltiplas réplicas.
		LeaderElection:   true,
		LeaderElectionID: "deployguard-leader-election",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// CONTROLLER: registra o reconcile loop do DeployPolicy.
	if err = (&controller.DeployPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create DeployPolicy controller")
		os.Exit(1)
	}

	// CERT ROTATOR: registra como Runnable.
	// O manager chama Start(ctx) em goroutine separada, após o cache sincronizar.
	rotator := &certrotator.Rotator{
		Client:                mgr.GetClient(),
		Namespace:             namespace,
		SecretName:            "deployguard-webhook-cert",
		ValidatingWebhookName: "deployguard-validating-webhook",
		CertDir:               certDir,
		ServiceName:           "deployguard-webhook-service",
	}
	if err = mgr.Add(rotator); err != nil {
		setupLog.Error(err, "unable to add cert rotator")
		os.Exit(1)
	}

	// WEBHOOK SERVER: registra como Runnable.
	webhookServer := &webhook.Server{
		Client:  mgr.GetClient(),
		Port:    webhookPort,
		CertDir: certDir,
	}
	if err = mgr.Add(webhookServer); err != nil {
		setupLog.Error(err, "unable to add webhook server")
		os.Exit(1)
	}

	// HEALTH CHECKS: o Kubernetes usa isso para decidir se o pod está pronto.
	// AddHealthzCheck: liveness — o processo está vivo?
	// AddReadyzCheck: readiness — o pod está pronto para receber tráfego?
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add readyz check")
		os.Exit(1)
	}

	setupLog.Info("starting deployguard operator",
		"namespace", namespace,
		"webhook_port", webhookPort,
	)

	// Start bloqueia aqui.
	// O manager inicia todos os Runnables em goroutines e espera SIGTERM.
	// Quando recebe SIGTERM, cancela o ctx e aguarda todos finalizarem (graceful shutdown).
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// isDev retorna true se a variável ENV indica ambiente de desenvolvimento.
func isDev() bool {
	return os.Getenv("ENV") != "production"
}

// getEnvOrDefault retorna o valor da env var ou o default se não estiver definida.
func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
