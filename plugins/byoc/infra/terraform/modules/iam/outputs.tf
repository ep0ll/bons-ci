output "dynamic_group_id" {
  description = "OCID of the runner instances dynamic group."
  value       = oci_identity_dynamic_group.runners.id
}

output "vault_policy_id" {
  description = "OCID of the Vault read IAM policy."
  value       = oci_identity_policy.runner_vault_read.id
}

output "artifact_bucket_name" {
  description = "Name of the Object Storage bucket for runner artifacts."
  value       = oci_objectstorage_bucket.artifacts.name
}
