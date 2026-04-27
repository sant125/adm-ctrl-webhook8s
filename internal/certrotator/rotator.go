// Package certrotator gerencia o certificado TLS do webhook server.
//
// POR QUE PRECISAMOS DISSO:
// O API server só chama nosso webhook se confiar no TLS dele.
// Para isso, ele precisa do CA certificate no campo "caBundle" do
// ValidatingWebhookConfiguration e MutatingWebhookConfiguration.
//
// Opções:
// 1. cert-manager (dependência externa)
// 2. Gerar manualmente e colocar como Secret (não rotaciona)
// 3. Este rotator (auto-suficiente, rotaciona automaticamente) ← fazemos isso
package certrotator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Rotator gera, salva e renova o certificado TLS do webhook.
// Implementa manager.Runnable.
type Rotator struct {
	// Client para interagir com o Kubernetes (atualizar caBundle).
	Client client.Client

	// Namespace onde o Secret com o cert é salvo.
	Namespace string

	// SecretName é o nome do Secret que armazena tls.crt e tls.key.
	SecretName string

	// ValidatingWebhookName é o nome do ValidatingWebhookConfiguration a ser patchado.
	ValidatingWebhookName string

	// CertDir é o diretório onde o webhook server lê os arquivos de cert.
	CertDir string

	// ServiceName é o nome do Service do webhook (usado no SAN do cert).
	// O API server conecta via "https://ServiceName.Namespace.svc"
	ServiceName string
}

// Start é chamado pelo manager. Roda o loop de rotação até ctx ser cancelado.
func (r *Rotator) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("cert-rotator")

	// Garante que existe um cert válido antes de o webhook server iniciar.
	logger.Info("ensuring initial certificate")
	if err := r.ensureCert(ctx); err != nil {
		return fmt.Errorf("initial cert generation failed: %w", err)
	}

	// Verifica rotação a cada 24h.
	// Na prática, rota apenas quando falta menos de 30 dias para expirar.
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logger.V(1).Info("checking certificate expiration")
			if err := r.rotateIfNeeded(ctx); err != nil {
				// Logar e continuar — não queremos derrubar o operator por falha de rotação.
				// Na próxima iteração tentará novamente.
				logger.Error(err, "cert rotation failed, will retry in 24h")
			}

		case <-ctx.Done():
			logger.Info("cert rotator stopping")
			return nil
		}
	}
}

// ensureCert verifica se existe cert válido; gera um novo se não existir.
func (r *Rotator) ensureCert(ctx context.Context) error {
	// Tenta carregar o cert existente do Secret.
	existing, err := r.loadCertFromSecret(ctx)
	if err == nil && existing != nil {
		// Cert existe — salva no disco e re-patcha o caBundle.
		// O caBundle precisa ser re-aplicado a cada restart do pod porque
		// outro controller pode ter sobrescrito o ValidatingWebhookConfiguration.
		if err := r.writeCertToDisk(existing.certPEM, existing.keyPEM); err != nil {
			return err
		}
		return r.patchWebhookConfigs(ctx, existing.caPEM)
	}

	// Não existe ou erro ao carregar → gera novo.
	return r.generateAndSave(ctx)
}

// rotateIfNeeded renova o cert se ele expira em menos de 30 dias.
func (r *Rotator) rotateIfNeeded(ctx context.Context) error {
	existing, err := r.loadCertFromSecret(ctx)
	if err != nil {
		return r.generateAndSave(ctx)
	}

	// Parse do cert para verificar validade.
	block, _ := pem.Decode(existing.certPEM)
	if block == nil {
		return r.generateAndSave(ctx)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return r.generateAndSave(ctx)
	}

	// Renova se expira em menos de 30 dias.
	if time.Until(cert.NotAfter) < 30*24*time.Hour {
		log.FromContext(ctx).Info("certificate expiring soon, rotating",
			"expires_at", cert.NotAfter)
		return r.generateAndSave(ctx)
	}

	return nil
}

// generateAndSave gera um novo cert self-signed, salva no Secret e atualiza o caBundle.
func (r *Rotator) generateAndSave(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("cert-rotator")
	logger.Info("generating new certificate")

	certPEM, keyPEM, caPEM, err := r.generateCert()
	if err != nil {
		return fmt.Errorf("generating cert: %w", err)
	}

	// Salva no Secret do Kubernetes (persistência entre restarts do pod).
	if err := r.saveCertToSecret(ctx, certPEM, keyPEM, caPEM); err != nil {
		return fmt.Errorf("saving cert to secret: %w", err)
	}

	// Salva no disco para o webhook server carregar via GetCertificate.
	if err := r.writeCertToDisk(certPEM, keyPEM); err != nil {
		return fmt.Errorf("writing cert to disk: %w", err)
	}

	// PASSO CRÍTICO: atualiza o caBundle nos WebhookConfigurations.
	// Sem isso, o API server não vai confiar no nosso cert e vai rejeitar todas as chamadas.
	if err := r.patchWebhookConfigs(ctx, caPEM); err != nil {
		return fmt.Errorf("patching webhook configs: %w", err)
	}

	logger.Info("certificate rotated successfully")
	return nil
}

// generateCert gera um par de chaves ECDSA e um certificado x509 self-signed.
//
// Por que ECDSA em vez de RSA?
// - Chaves menores com segurança equivalente
// - Operações de sign/verify mais rápidas
// - P256 é amplamente suportado
func (r *Rotator) generateCert() (certPEM, keyPEM, caPEM []byte, err error) {
	// Gera chave privada ECDSA P-256.
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating private key: %w", err)
	}

	// SERIAL NUMBER — 128 bits aleatórios.
	//
	// Por que não deixar fixo em 1?
	// - RFC 5280 exige serial único por CA. Dois certs com mesmo serial emitidos
	//   pela mesma CA tecnicamente são inválidos.
	// - Alguns clientes TLS (e scanners de segurança) sinalizam serial=1 como
	//   "cert auto-gerado de brinquedo" — acende flag vermelha em auditoria.
	// - Na rotação, cert antigo e novo teriam o MESMO serial — se alguém revogar
	//   via CRL/OCSP por serial, revogaria os dois. Irrelevante em self-signed,
	//   mas o costume certo já vale ser aprendido aqui.
	//
	// Como geramos: rand.Int(rand.Reader, 2^128) devolve inteiro aleatório
	// criptográfico no intervalo [0, 2^128). Lsh = left shift (1 << 128).
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating serial number: %w", err)
	}

	// Template do certificado.
	// O SAN (Subject Alternative Name) deve incluir o DNS do Service
	// porque é assim que o API server se conecta ao webhook.
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"deployguard"},
		},
		// DNS SANs necessários para o API server aceitar o cert.
		// Formato: service.namespace.svc
		DNSNames: []string{
			fmt.Sprintf("%s.%s.svc", r.ServiceName, r.Namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", r.ServiceName, r.Namespace),
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour), // 1 ano de validade

		// KeyUsage define o que a chave pode fazer.
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},

		// IsCA: true porque usamos o mesmo cert como CA (self-signed).
		// Em produção, você teria uma CA separada assinando o cert do servidor.
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	// Gera o certificado DER (self-signed: assina com a própria chave).
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating certificate: %w", err)
	}

	// Serializa a chave privada para PEM.
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshaling private key: %w", err)
	}

	// Encodifica para PEM (formato que o Go e o Kubernetes entendem).
	var certBuf, keyBuf bytes.Buffer
	if err := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return nil, nil, nil, err
	}
	if err := pem.Encode(&keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return nil, nil, nil, err
	}

	certPEM = certBuf.Bytes()
	keyPEM = keyBuf.Bytes()
	caPEM = certPEM // self-signed: CA cert = server cert

	return certPEM, keyPEM, caPEM, nil
}

// patchWebhookConfigs atualiza o campo caBundle nos WebhookConfigurations via Strategic Merge Patch.
//
// Por que patch e não update?
// - Update sobrescreve o objeto inteiro — race condition com outros controllers
// - Patch é atômico e só muda o campo que queremos
func (r *Rotator) patchWebhookConfigs(ctx context.Context, caPEM []byte) error {
	// Patch para ValidatingWebhookConfiguration.
	var vwc admissionv1.ValidatingWebhookConfiguration
	if err := r.Client.Get(ctx, types.NamespacedName{Name: r.ValidatingWebhookName}, &vwc); err != nil {
		return fmt.Errorf("getting ValidatingWebhookConfiguration: %w", err)
	}

	// Atualiza o caBundle em todos os webhooks da configuração.
	for i := range vwc.Webhooks {
		vwc.Webhooks[i].ClientConfig.CABundle = caPEM
	}

	if err := r.Client.Update(ctx, &vwc); err != nil {
		return fmt.Errorf("updating ValidatingWebhookConfiguration caBundle: %w", err)
	}

	return nil
}

// certData agrupa cert e key PEM para passar entre funções.
type certData struct {
	certPEM []byte
	keyPEM  []byte
	caPEM   []byte
}

func (r *Rotator) loadCertFromSecret(ctx context.Context) (*certData, error) {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{
		Namespace: r.Namespace,
		Name:      r.SecretName,
	}, &secret); err != nil {
		return nil, err
	}

	return &certData{
		certPEM: secret.Data["tls.crt"],
		keyPEM:  secret.Data["tls.key"],
		caPEM:   secret.Data["ca.crt"],
	}, nil
}

func (r *Rotator) saveCertToSecret(ctx context.Context, certPEM, keyPEM, caPEM []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.Namespace,
			Name:      r.SecretName,
		},
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
			"ca.crt":  caPEM,
		},
	}

	// CreateOrUpdate: cria se não existe, atualiza se existe.
	// Evita ter que tratar os dois casos separadamente.
	existing := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: r.SecretName}, existing)
	if err != nil {
		return r.Client.Create(ctx, secret)
	}

	existing.Data = secret.Data
	return r.Client.Update(ctx, existing)
}

func (r *Rotator) writeCertToDisk(certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(r.CertDir, 0700); err != nil {
		return fmt.Errorf("creating cert dir: %w", err)
	}

	if err := os.WriteFile(fmt.Sprintf("%s/tls.crt", r.CertDir), certPEM, 0600); err != nil {
		return fmt.Errorf("writing tls.crt: %w", err)
	}

	if err := os.WriteFile(fmt.Sprintf("%s/tls.key", r.CertDir), keyPEM, 0600); err != nil {
		return fmt.Errorf("writing tls.key: %w", err)
	}

	return nil
}
