#!/usr/bin/env bash
# bootstrap.sh — instala toda a stack de observabilidade no cluster.
# Compatível com GKE Autopilot.
#
# Uso:
#   chmod +x bootstrap.sh
#   ./bootstrap.sh
#
# Pré-requisitos: helm, kubectl configurados e apontando pro cluster certo.

set -euo pipefail

echo "==> Adding Helm repos..."
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

echo "==> Creating namespaces..."
kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace observability --dry-run=client -o yaml | kubectl apply -f -

# ============================================================
# 1. kube-prometheus-stack (Prometheus + Grafana + Alertmanager)
#
# Flags de compatibilidade com GKE Autopilot:
# - nodeExporter desabilitado (requer hostPath/hostNetwork/hostPID)
# - kubeEtcd/Scheduler/ControllerManager/Proxy desabilitados (managed pelo GKE)
# - coreDns desabilitado (managed pelo GKE)
# ============================================================
echo "==> Installing kube-prometheus-stack..."
helm upgrade --install kube-prom prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --set grafana.adminPassword=admin \
  --set nodeExporter.enabled=false \
  --set kubeEtcd.enabled=false \
  --set kubeScheduler.enabled=false \
  --set kubeControllerManager.enabled=false \
  --set kubeProxy.enabled=false \
  --set coreDns.enabled=false \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --timeout 5m \
  --wait

# ============================================================
# 2. Tempo (backend de traces — substitui Jaeger)
#
# monolithic mode: tudo numa pod só, storage em memória.
# Para produção: usar GCS como backend de storage.
# ============================================================
echo "==> Installing Tempo..."
helm upgrade --install tempo grafana/tempo \
  --namespace monitoring \
  --set tempo.storage.trace.backend=local \
  --set tempo.resources.requests.cpu=100m \
  --set tempo.resources.requests.memory=256Mi \
  --wait

# ============================================================
# 3. Loki + Promtail (logs)
#
# Loki: armazena logs.
# Promtail: DaemonSet que coleta logs dos pods via /var/log/
#           (único hostPath permitido no Autopilot)
# ============================================================
echo "==> Installing Loki..."
helm upgrade --install loki grafana/loki \
  --namespace monitoring \
  --set loki.auth_enabled=false \
  --set deploymentMode=SingleBinary \
  --set singleBinary.replicas=1 \
  --set loki.storage.type=filesystem \
  --set loki.commonConfig.replication_factor=1 \
  --wait

echo "==> Installing Promtail..."
helm upgrade --install promtail grafana/promtail \
  --namespace monitoring \
  --set config.lokiAddress=http://loki:3100/loki/api/v1/push \
  --wait

# ============================================================
# 4. OTEL Collector + datasources Grafana
# ============================================================
echo "==> Applying OTEL Collector (Tempo exporter)..."
kubectl apply -f "$(dirname "$0")/../otel-collector/collector.yaml"

echo "==> Applying Grafana datasources..."
kubectl apply -f "$(dirname "$0")/grafana-datasources.yaml"

echo ""
echo "==> Done! Stack running in namespace 'monitoring'."
echo ""
echo "Access Grafana:"
echo "  kubectl port-forward -n monitoring svc/kube-prom-grafana 3000:80 --address 0.0.0.0"
echo "  http://localhost:3000  (admin / admin)"
