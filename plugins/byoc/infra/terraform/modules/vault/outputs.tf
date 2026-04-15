output "vault_id" {
  description = "OCID of the OCI Vault."
  value       = oci_kms_vault.this.id
}

output "vault_management_endpoint" {
  description = "Vault management endpoint URL."
  value       = oci_kms_vault.this.management_endpoint
}

output "master_key_id" {
  description = "OCID of the AES-256 master encryption key."
  value       = oci_kms_key.master.id
}

output "db_password_secret_id" {
  description = "OCID of the DB password secret."
  value       = oci_vault_secret.db_password.id
}
