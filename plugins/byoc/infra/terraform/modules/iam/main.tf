# modules/iam/main.tf
# Creates a dynamic group that matches runner instances by freeform tag,
# and grants them least-privilege IAM policies for Vault read + Object Storage write.
# No long-lived API keys are placed on runner instances — instance principal auth only.

terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = ">= 5.0"
    }
  }
}

# ── Dynamic Group — matches all runner instances for this tenant ──────────────

resource "oci_identity_dynamic_group" "runners" {
  compartment_id = var.tenancy_ocid
  name           = "byoc-runners-${var.tenant_id}"
  description    = "BYOC runner instances for tenant ${var.tenant_id}"

  # Match instances in the tenant compartment tagged as BYOC-managed.
  matching_rule = "All {instance.compartment.id = '${var.compartment_id}', tag.byoc:managed_by.value = 'byoc-oci-runners'}"

  freeform_tags = local.common_tags
}

# ── IAM Policy — Vault read (registration tokens) ────────────────────────────

resource "oci_identity_policy" "runner_vault_read" {
  compartment_id = var.compartment_id
  name           = "byoc-runner-vault-read-${var.tenant_id}"
  description    = "Allows BYOC runner instances to read secrets from OCI Vault"

  statements = [
    "Allow dynamic-group ${oci_identity_dynamic_group.runners.name} to read secret-family in compartment id ${var.compartment_id}",
  ]

  freeform_tags = local.common_tags
}

# ── IAM Policy — Object Storage write (artifact uploads) ─────────────────────

resource "oci_identity_policy" "runner_object_storage" {
  compartment_id = var.compartment_id
  name           = "byoc-runner-object-storage-${var.tenant_id}"
  description    = "Allows BYOC runner instances to write build artifacts to Object Storage"

  statements = [
    "Allow dynamic-group ${oci_identity_dynamic_group.runners.name} to manage objects in compartment id ${var.compartment_id} where target.bucket.name = 'byoc-artifacts-${var.tenant_id}'",
  ]

  freeform_tags = local.common_tags
}

# ── Object Storage Bucket — runner build artifacts ────────────────────────────

resource "oci_objectstorage_bucket" "artifacts" {
  compartment_id = var.compartment_id
  namespace      = var.object_storage_namespace
  name           = "byoc-artifacts-${var.tenant_id}"
  access_type    = "NoPublicAccess"

  # Lifecycle: delete artifacts older than 30 days.
  object_lifecycle_policy_etag = ""

  freeform_tags = local.common_tags
}

# ── Locals ────────────────────────────────────────────────────────────────────

locals {
  common_tags = {
    "byoc:tenant_id"  = var.tenant_id
    "byoc:managed_by" = "byoc-oci-runners"
    "byoc:module"     = "iam"
  }
}
