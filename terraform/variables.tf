variable "project_id" {
  description = "GCP project ID"
  type        = string
  default     = "certquest"
}

variable "region" {
  description = "GCP region"
  type        = string
  default     = "us-east1"
}

variable "cluster_name" {
  description = "GKE cluster name"
  type        = string
  default     = "deployguard"
}
