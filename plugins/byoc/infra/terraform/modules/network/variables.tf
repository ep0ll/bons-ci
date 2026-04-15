variable "compartment_id" {
  description = "OCI compartment OCID for this tenant's resources."
  type        = string
}

variable "tenant_id" {
  description = "Platform tenant UUID — used in resource display names and tags."
  type        = string
}

variable "vcn_cidr" {
  description = "CIDR block for the tenant VCN."
  type        = string
  default     = "10.0.0.0/16"
}

variable "private_subnet_cidr" {
  description = "CIDR block for the runner private subnet."
  type        = string
  default     = "10.0.1.0/24"
}

variable "public_subnet_cidr" {
  description = "CIDR block for the public subnet (load balancer / bastion)."
  type        = string
  default     = "10.0.0.0/24"
}
