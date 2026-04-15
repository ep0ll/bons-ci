# environments/dev/main.tf
# Root module for the development environment.
# Wires network, IAM, vault, database, and monitoring modules together.

terraform {
  required_version = ">= 1.7"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = ">= 5.0"
    }
  }

  # Remote state: OCI Object Storage backend.
  # Uncomment and fill in for real deployments.
  # backend "s3" {
  #   bucket   = "byoc-terraform-state"
  #   key      = "dev/terraform.tfstate"
  #   region   = "us-ashburn-1"
  #   endpoint = "https://<namespace>.compat.objectstorage.us-ashburn-1.oraclecloud.com"
  # }
}

provider "oci" {
  region = var.oci_region
  # Auth via instance principal when running inside OCI;
  # auth = "InstancePrincipal"
  # For local dev use API key auth (default).
}

# ── Shared Vault (one per environment, not per tenant) ───────────────────────

module "vault" {
  source         = "../../modules/vault"
  compartment_id = var.compartment_id
  environment    = "dev"
}

# ── Control Plane Network (shared) ───────────────────────────────────────────

module "control_plane_network" {
  source              = "../../modules/network"
  compartment_id      = var.compartment_id
  tenant_id           = "control-plane"
  vcn_cidr            = "10.100.0.0/16"
  private_subnet_cidr = "10.100.1.0/24"
  public_subnet_cidr  = "10.100.0.0/24"
}

# ── MySQL (control plane state) ───────────────────────────────────────────────

module "database" {
  source                 = "../../modules/database"
  compartment_id         = var.compartment_id
  environment            = "dev"
  availability_domain    = var.availability_domain
  private_subnet_id      = module.control_plane_network.private_subnet_id
  db_password_secret_id  = module.vault.db_password_secret_id
  db_shape               = "MySQL.VM.Standard.E4.1.8GB"
  storage_gb             = 50
}

# ── Example Tenant Network (acme-corp) ────────────────────────────────────────

module "tenant_acme_network" {
  source              = "../../modules/network"
  compartment_id      = var.tenant_acme_compartment_id
  tenant_id           = "acme-corp"
  vcn_cidr            = "10.1.0.0/16"
  private_subnet_cidr = "10.1.1.0/24"
  public_subnet_cidr  = "10.1.0.0/24"
}

module "tenant_acme_iam" {
  source                   = "../../modules/iam"
  tenancy_ocid             = var.tenancy_ocid
  compartment_id           = var.tenant_acme_compartment_id
  tenant_id                = "acme-corp"
  object_storage_namespace = var.object_storage_namespace
}

# ── Monitoring ────────────────────────────────────────────────────────────────

module "monitoring" {
  source         = "../../modules/monitoring"
  compartment_id = var.compartment_id
  environment    = "dev"
  alert_email    = var.alert_email
}

# ── Outputs ───────────────────────────────────────────────────────────────────

output "db_endpoint" {
  value = module.database.db_endpoint
}

output "db_port" {
  value = module.database.db_port
}

output "vault_id" {
  value = module.vault.vault_id
}

output "control_plane_private_subnet_id" {
  value = module.control_plane_network.private_subnet_id
}

output "tenant_acme_private_subnet_id" {
  value = module.tenant_acme_network.private_subnet_id
}
