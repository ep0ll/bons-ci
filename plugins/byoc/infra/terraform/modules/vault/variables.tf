variable "compartment_id" {
  description = "OCI compartment OCID where the Vault is created."
  type        = string
}

variable "environment" {
  description = "Deployment environment label (dev | staging | prod)."
  type        = string
  default     = "dev"
}
