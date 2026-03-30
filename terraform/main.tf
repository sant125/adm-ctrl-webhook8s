terraform {
  required_version = ">= 1.7"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# APIs necessárias
resource "google_project_service" "apis" {
  for_each = toset([
    "container.googleapis.com",        # GKE
    "artifactregistry.googleapis.com", # Artifact Registry
    "cloudbuild.googleapis.com",       # Cloud Build
    "cloudtrace.googleapis.com",       # Cloud Trace (OTEL backend)
    "iam.googleapis.com",              # IAM (Workload Identity)
  ])
  service            = each.value
  disable_on_destroy = false
}

# Google Service Account do operator.
# O pod vai assumir essa identidade via Workload Identity — sem chave JSON.
resource "google_service_account" "operator" {
  account_id   = "deployguard-operator"
  display_name = "DeployGuard Operator"
  project      = var.project_id
}

# Permissão pra escrever traces no Cloud Trace.
resource "google_project_iam_member" "trace_agent" {
  project = var.project_id
  role    = "roles/cloudtrace.agent"
  member  = "serviceAccount:${google_service_account.operator.email}"
}

# Workload Identity: vincula o K8s ServiceAccount ao Google Service Account.
# O pod não precisa de chave JSON — a identidade é provada pelo token do K8s.
resource "google_service_account_iam_member" "workload_identity" {
  service_account_id = google_service_account.operator.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[deployguard-system/deployguard-controller]"

  depends_on = [google_container_cluster.this]
}

# Artifact Registry — registry privado para a imagem do operator.
# format=DOCKER, regional (us-central1) igual ao cluster.
resource "google_artifact_registry_repository" "this" {
  repository_id = "deployguard"
  location      = var.region
  format        = "DOCKER"
  project       = var.project_id

  depends_on = [google_project_service.apis]
}

# GKE Standard — acesso total ao node, sem restrições do Autopilot.
resource "google_container_cluster" "this" {
  name     = var.cluster_name
  location = var.region
  project  = var.project_id

  remove_default_node_pool = true
  initial_node_count       = 1

  # Define pd-standard no pool inicial (temporário) pra não estourar quota de SSD.
  node_config {
    disk_type    = "pd-standard"
    disk_size_gb = 30
    machine_type = "e2-medium"
  }

  deletion_protection = false

  depends_on = [google_project_service.apis]
}

# Node pool com 2 nós e2-standard-2 (2 vCPU, 8GB) — suficiente pra stack completa.
resource "google_container_node_pool" "main" {
  name       = "main"
  cluster    = google_container_cluster.this.name
  location   = var.region
  project    = var.project_id
  node_count = 2

  node_config {
    machine_type = "e2-standard-2"
    disk_size_gb = 50
    disk_type    = "pd-standard"
    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }
}
