# modules/database/main.tf
# Provisions OCI MySQL HeatWave (managed MySQL) for control-plane state.
# The instance is placed in the private subnet; the admin password is
# sourced from OCI Vault — never from a Terraform variable.

terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = ">= 5.0"
    }
  }
}

resource "oci_mysql_mysql_db_system" "control_plane" {
  compartment_id      = var.compartment_id
  display_name        = "byoc-db-${var.environment}"
  availability_domain = var.availability_domain
  subnet_id           = var.private_subnet_id
  shape_name          = var.db_shape  # e.g. "MySQL.VM.Standard.E4.1.8GB"

  admin_username = "byoc_admin"
  # Admin password sourced from Vault at provisioning time via data source.
  admin_password = data.oci_secrets_secretbundle.db_password.secret_bundle_content[0].content

  data_storage_size_in_gb = var.storage_gb

  backup_policy {
    is_enabled        = true
    retention_in_days = 7
    window_start_time = "02:00"
  }

  deletion_policy {
    automatic_backup_retention = "RETAIN"
    final_backup               = "REQUIRE_FINAL_BACKUP"
    is_delete_protected        = true
  }

  freeform_tags = local.common_tags
}

# ── Fetch DB password from Vault (not stored in Terraform state) ──────────────

data "oci_secrets_secretbundle" "db_password" {
  secret_id = var.db_password_secret_id
}

# ── Locals ────────────────────────────────────────────────────────────────────

locals {
  common_tags = {
    "byoc:managed_by" = "byoc-oci-runners"
    "byoc:module"     = "database"
    "byoc:env"        = var.environment
  }
}
