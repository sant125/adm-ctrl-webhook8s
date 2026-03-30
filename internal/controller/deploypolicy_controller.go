// Package controller implementa o reconcile loop do CRD DeployPolicy.
//
// O RECONCILE LOOP:
// O controller-runtime chama Reconcile() toda vez que:
// - Um DeployPolicy é criado, atualizado ou deletado
// - O operator restarta (reconcilia todos os objetos existentes)
// - Um requeue é solicitado (ctrl.Result{RequeueAfter: ...})
//
// PRINCÍPIO FUNDAMENTAL: Reconcile deve ser IDEMPOTENTE.
// Rodado 1 vez ou 100 vezes com o mesmo estado → mesmo resultado.
package controller

import (
	"context"
	"fmt"

	"github.com/santzin/deployguard/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// finalizerName identifica nosso finalizer.
	// Convenção: "domain/nome-do-finalizer"
	// O finalizer impede que o objeto seja deletado antes de fazermos cleanup.
	finalizerName = "deployguard.io/policy-finalizer"
)

// DeployPolicyReconciler implementa o reconcile loop.
// O controller-runtime injeta o Client e o Scheme via SetupWithManager.
type DeployPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile é chamado pelo controller-runtime quando um DeployPolicy muda.
//
// req.NamespacedName contém namespace/name do objeto que mudou.
// Retornar erro → requeue com backoff exponencial.
// Retornar ctrl.Result{} sem erro → não faz requeue (tudo ok).
// Retornar ctrl.Result{RequeueAfter: d} → requeue depois de d.
func (r *DeployPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("deploypolicy", req.NamespacedName)

	// PASSO 1: Busca o objeto atual no cluster.
	var policy v1alpha1.DeployPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			// Objeto foi deletado entre o evento e o Reconcile.
			// Isso é normal — apenas ignoramos.
			logger.V(1).Info("DeployPolicy not found, probably deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting DeployPolicy: %w", err)
	}

	// PASSO 2: Checa se está sendo deletado (DeletionTimestamp != zero).
	// O Kubernetes não deleta o objeto enquanto tiver finalizers.
	// Nós removemos o finalizer após o cleanup.
	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &policy)
	}

	// PASSO 3: Garante que o finalizer está presente.
	// Na primeira vez que o objeto é criado, ele não tem finalizer.
	if !controllerutil.ContainsFinalizer(&policy, finalizerName) {
		controllerutil.AddFinalizer(&policy, finalizerName)
		// Update persiste o finalizer no etcd. Isso vai triggar outro Reconcile,
		// mas na próxima passada o finalizer já vai estar presente.
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// PASSO 4: Lógica de reconciliação principal.
	if err := r.reconcilePolicy(ctx, &policy); err != nil {
		logger.Error(err, "failed to reconcile policy")

		// Atualiza o status com a condição de erro.
		// O operador de Status().Update() usa o subresource /status
		// para não conflitar com updates no spec.
		setCondition(&policy, "Ready", metav1.ConditionFalse, "ReconcileError", err.Error())
		if statusErr := r.Status().Update(ctx, &policy); statusErr != nil {
			logger.Error(statusErr, "failed to update status after error")
		}

		return ctrl.Result{}, err
	}

	// PASSO 5: Atualiza status com sucesso.
	setCondition(&policy, "Ready", metav1.ConditionTrue, "Reconciled", "Policy is active")
	if err := r.Status().Update(ctx, &policy); err != nil {
		// Falha ao atualizar status não é crítico — o objeto está reconciliado.
		// Log e não retorna erro para não fazer requeue desnecessário.
		logger.Error(err, "failed to update status")
	}

	logger.Info("DeployPolicy reconciled successfully")
	return ctrl.Result{}, nil
}

// handleDeletion faz o cleanup quando o objeto está sendo deletado.
// Após o cleanup, remove o finalizer para o Kubernetes poder deletar o objeto.
func (r *DeployPolicyReconciler) handleDeletion(ctx context.Context, policy *v1alpha1.DeployPolicy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handling DeployPolicy deletion")

	// TODO: adicione cleanup aqui se necessário.
	// Exemplos: remover PrometheusRules criadas, notificar times, etc.

	// Remove o finalizer — o Kubernetes vai prosseguir com a deleção.
	controllerutil.RemoveFinalizer(policy, finalizerName)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// reconcilePolicy implementa a lógica de negócio do reconcile.
// Separar do Reconcile principal facilita testes e legibilidade.
func (r *DeployPolicyReconciler) reconcilePolicy(ctx context.Context, policy *v1alpha1.DeployPolicy) error {
	logger := log.FromContext(ctx)

	// Por enquanto, a DeployPolicy é puramente declarativa:
	// o webhook lê a policy diretamente do cluster quando avalia um Deployment.
	// Não há recursos filho para criar/sincronizar.
	//
	// Aqui você pode adicionar:
	// - Validação extra do spec (ex: checar se os valores de CPU são parseable)
	// - Criar recursos auxiliares (ex: PrometheusRule para métricas da policy)
	// - Notificações (ex: Slack quando policy é criada/atualizada)

	logger.V(1).Info("policy spec is valid",
		"requireResources", policy.Spec.RequireResources,
		"blockLatestTag", policy.Spec.BlockLatestTag,
		"requireReadinessProbe", policy.Spec.RequireReadinessProbe,
	)

	return nil
}

// setCondition é um helper para atualizar conditions no status.
// Usamos o padrão de Conditions do Kubernetes (mesmo que Node, Pod, Deployment).
func setCondition(policy *v1alpha1.DeployPolicy, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

// SetupWithManager registra o controller no manager.
// "Watches" define quais objetos trigam o Reconcile.
func (r *DeployPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Assiste DeployPolicy — qualquer mudança nesse tipo chama Reconcile.
		For(&v1alpha1.DeployPolicy{}).
		Complete(r)
}
