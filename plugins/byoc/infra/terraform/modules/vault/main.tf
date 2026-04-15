# modules/vault/main.tf
# Provisions an OCI Vault for the control-plane to store GitHub App private keys,
# webhook secrets, and database passwords.  Secrets are never written to Terraform state —
# the initial values must be populated post-apply via OCI CLI / API.

terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = ">= 5.0"
    }
  }
}

# ── OCI Vault (Virtual Private Vault — HSM-backed) ────────────────────────────

resource "oci_kms_vault" "this" {
  compartment_id = var.compartment_id
  display_name   = "byoc-vault-${var.environment}"
  vault_type     = "DEFAULT" # Use "VIRTUAL_PRIVATE" for prod HSM

  freeform_tags = local.common_tags
}

# ── Master Encryption Key ─────────────────────────────────────────────────────

resource "oci_kms_key" "master" {
  compartment_id      = var.compartment_id
  display_name        = "byoc-master-key-${var.environment}"
  management_endpoint = oci_kms_vault.this.management_endpoint

  key_shape {
    algorithm = "AES"
    length    = 32 # AES-256
  }

  freeform_tags = local.common_tags
}

# ── Secret placeholders ───────────────────────────────────────────────────────
# These create empty secret shells; actual values are populated post-deploy via:
#   oci vault secret create-base64 --compartment-id ... --secret-name ... --secret-content-content <base64>
# They are defined here so the Vault OCID mapping in Go config can be static.

resource "oci_vault_secret" "db_password" {
  compartment_id = var.compartment_id
  vault_id       = oci_kms_vault.this.id
  key_id         = oci_kms_key.master.id
  secret_name    = "byoc-db-password-${var.environment}"

  secret_content {
    content_type = "BASE64"
    # Placeholder — MUST be rotated immediately after first apply.
    content = base64encode("CHANGE_ME_IMMEDIATELY")
    name    = "initial"
    stage   = "CURRENT"
  }

  freeform_tags = local.common_tags

  lifecycle {
    # Prevent Terraform from overwriting a real secret value on re-apply.
    ignore_changes = [secret_content]
  }
}

# ── Locals ────────────────────────────────────────────────────────────────────

locals {
  common_tags = {
    "byoc:managed_by" = "byoc-oci-runners"
    "byoc:module"     = "vault"
    "byoc:env"        = var.environment
  }
}
