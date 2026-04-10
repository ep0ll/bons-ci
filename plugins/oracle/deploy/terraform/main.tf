# ─────────────────────────────────────────────────────────────────────────────
# Terraform: OCI infrastructure for the preemptible live-migration system
#
# Provisions:
#   • VCN + subnet + security groups
#   • Shared migration block volume (ext4, 100 GB)
#   • Custom image with CRIU + migrator agent pre-baked
#   • Instance configuration for preemptible builds
#   • IAM dynamic group + policy for instance-principal auth
# ─────────────────────────────────────────────────────────────────────────────

terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "~> 5.0"
    }
  }
}

variable "tenancy_ocid"     {}
variable "compartment_ocid" {}
variable "region"           { default = "us-ashburn-1" }
variable "ad_name"          { default = "aBCD:US-ASHBURN-AD-1" }
variable "ssh_public_key"   {}

provider "oci" {
  region = var.region
  # Uses instance principal or env vars in CI.
}

# ─── Networking ──────────────────────────────────────────────────────────────

resource "oci_core_vcn" "cicd" {
  compartment_id = var.compartment_ocid
  cidr_blocks    = ["10.100.0.0/16"]
  display_name   = "cicd-vcn"
  dns_label      = "cicd"
}

resource "oci_core_subnet" "build" {
  compartment_id    = var.compartment_ocid
  vcn_id            = oci_core_vcn.cicd.id
  cidr_block        = "10.100.1.0/24"
  display_name      = "build-subnet"
  dns_label         = "build"
  prohibit_public_ip_on_vnic = true
}

resource "oci_core_network_security_group" "migrator" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.cicd.id
  display_name   = "migrator-nsg"
}

# Allow CRIU page-server traffic between build instances.
resource "oci_core_network_security_group_security_rule" "page_server_ingress" {
  network_security_group_id = oci_core_network_security_group.migrator.id
  direction                 = "INGRESS"
  protocol                  = "6" # TCP
  source                    = "10.100.1.0/24"
  source_type               = "CIDR_BLOCK"
  tcp_options {
    destination_port_range {
      min = 27182
      max = 27182
    }
  }
  description = "CRIU page-server (source → successor memory streaming)"
}

# ─── Shared Migration Block Volume ───────────────────────────────────────────

resource "oci_core_volume" "migration_shared" {
  compartment_id      = var.compartment_ocid
  availability_domain = var.ad_name
  display_name        = "migration-shared-vol"
  size_in_gbs         = 100

  # Use balanced performance — sufficient for checkpoint I/O without
  # paying for ultra-high-performance storage.
  vpus_per_gb = 10

  freeform_tags = {
    "managed-by" = "oci-live-migrator"
    "purpose"    = "criu-checkpoint-storage"
  }
}

# ─── IAM: Instance Principal for Migrator ────────────────────────────────────

resource "oci_identity_dynamic_group" "migrators" {
  name           = "cicd-migrators"
  compartment_id = var.tenancy_ocid
  description    = "Build instances running the live migrator daemon"
  matching_rule  = "tag.managed-by.value='oci-live-migrator'"
}

resource "oci_identity_policy" "migrator_policy" {
  name           = "cicd-migrator-policy"
  compartment_id = var.tenancy_ocid
  description    = "Allow migrator daemon to manage instances and volumes"

  statements = [
    # Instance management.
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to manage instances in compartment id ${var.compartment_ocid}",
    # Boot volume access (to preserve and reuse across preemptions).
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to manage boot-volumes in compartment id ${var.compartment_ocid}",
    # Block volume hot-attach / detach.
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to manage volume-attachments in compartment id ${var.compartment_ocid}",
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to use volumes in compartment id ${var.compartment_ocid}",
    # VCN / VNIC read for IP assignment.
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to use vnics in compartment id ${var.compartment_ocid}",
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to read subnets in compartment id ${var.compartment_ocid}",
    # Vault (optional): read migration secrets.
    "Allow dynamic-group ${oci_identity_dynamic_group.migrators.name} to read secret-family in compartment id ${var.compartment_ocid}",
  ]
}

# ─── Outputs ─────────────────────────────────────────────────────────────────

output "shared_volume_ocid" {
  value = oci_core_volume.migration_shared.id
}

output "build_subnet_ocid" {
  value = oci_core_subnet.build.id
}

output "migrator_nsg_ocid" {
  value = oci_core_network_security_group.migrator.id
}
