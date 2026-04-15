variable "tenancy_ocid" {
  description = "Root tenancy OCID — required for dynamic group creation."
  type        = string
}

variable "compartment_id" {
  description = "OCI compartment OCID for this tenant."
  type        = string
}

variable "tenant_id" {
  description = "Platform tenant UUID."
  type        = string
}

variable "object_storage_namespace" {
  description = "OCI Object Storage namespace (tenancy-level, not compartment)."
  type        = string
}
