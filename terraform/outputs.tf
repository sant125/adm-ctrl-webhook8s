output "cluster_name" {
  value = google_container_cluster.this.name
}

output "region" {
  value = google_container_cluster.this.location
}

output "get_credentials" {
  description = "Comando para configurar o kubeconfig"
  value       = "gcloud container clusters get-credentials ${google_container_cluster.this.name} --region ${google_container_cluster.this.location} --project ${var.project_id}"
}

output "image_base" {
  description = "Base URL da imagem no Artifact Registry"
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.this.repository_id}/deployguard"
}

output "operator_gsa" {
  description = "Google Service Account do operator (para annotation do K8s SA)"
  value       = google_service_account.operator.email
}
