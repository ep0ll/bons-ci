variable "oci_region" {
  description = "OCI region identifier."
  type        = string
  default     = "us-ashburn-1"
}

variable "tenancy_ocid" {
  description = "Root tenancy OCID."
  type        = string
}

variable "compartment_id" {
  description = "OCI compartment OCID for the control plane."
  type        = string
}

variable "availability_domain" {
  description = "OCI availability domain (e.g. 'Uocm:US-ASHBURN-AD-1')."
  type        = string
}

variable "object_storage_namespace" {
  description = "OCI Object Storage namespace."
  type        = string
}

variable "tenant_acme_compartment_id" {
  description = "Compartment OCID for the example 'acme-corp' tenant."
  type        = string
  default     = ""
}

variable "alert_email" {
  description = "Email for monitoring alerts."
  type        = string
  default     = ""
}
