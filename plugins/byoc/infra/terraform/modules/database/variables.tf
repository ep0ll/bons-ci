variable "compartment_id" { type = string }
variable "environment" { type = string; default = "dev" }
variable "availability_domain" { type = string }
variable "private_subnet_id" { type = string }
variable "db_password_secret_id" {
  description = "OCI Vault secret OCID for the DB admin password."
  type        = string
}
variable "db_shape" {
  type    = string
  default = "MySQL.VM.Standard.E4.1.8GB"
}
variable "storage_gb" {
  type    = number
  default = 50
}
