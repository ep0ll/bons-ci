# modules/network/main.tf
# Provisions the VCN, subnets, NAT gateway, and security groups for one tenant.
# Each tenant gets an isolated private network with no inbound from the public internet.

terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = ">= 5.0"
    }
  }
}

# ── VCN ─────────────────────────────────────────────────────────────────────

resource "oci_core_vcn" "this" {
  compartment_id = var.compartment_id
  display_name   = "vcn-byoc-${var.tenant_id}"
  cidr_blocks    = [var.vcn_cidr]

  freeform_tags = local.common_tags
}

# ── Internet Gateway (for public subnet / NAT) ───────────────────────────────

resource "oci_core_internet_gateway" "this" {
  compartment_id = var.compartment_id
  vcn_id         = oci_core_vcn.this.id
  display_name   = "igw-byoc-${var.tenant_id}"
  enabled        = true

  freeform_tags = local.common_tags
}

# ── NAT Gateway (private subnet outbound to GitHub) ──────────────────────────

resource "oci_core_nat_gateway" "this" {
  compartment_id = var.compartment_id
  vcn_id         = oci_core_vcn.this.id
  display_name   = "nat-byoc-${var.tenant_id}"
  block_traffic  = false

  freeform_tags = local.common_tags
}

# ── Service Gateway (OCI Services — Vault, Object Storage) ───────────────────

data "oci_core_services" "all" {}

resource "oci_core_service_gateway" "this" {
  compartment_id = var.compartment_id
  vcn_id         = oci_core_vcn.this.id
  display_name   = "sgw-byoc-${var.tenant_id}"

  services {
    service_id = data.oci_core_services.all.services[0].id
  }

  freeform_tags = local.common_tags
}

# ── Route Tables ─────────────────────────────────────────────────────────────

resource "oci_core_route_table" "public" {
  compartment_id = var.compartment_id
  vcn_id         = oci_core_vcn.this.id
  display_name   = "rt-public-${var.tenant_id}"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.this.id
  }

  freeform_tags = local.common_tags
}

resource "oci_core_route_table" "private" {
  compartment_id = var.compartment_id
  vcn_id         = oci_core_vcn.this.id
  display_name   = "rt-private-${var.tenant_id}"

  # Outbound via NAT (for GitHub API + runner download).
  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_nat_gateway.this.id
  }

  # OCI Services via Service Gateway (Vault, Object Storage).
  route_rules {
    destination       = data.oci_core_services.all.services[0].cidr_block
    destination_type  = "SERVICE_CIDR_BLOCK"
    network_entity_id = oci_core_service_gateway.this.id
  }

  freeform_tags = local.common_tags
}

# ── Security Lists ────────────────────────────────────────────────────────────

resource "oci_core_security_list" "runner" {
  compartment_id = var.compartment_id
  vcn_id         = oci_core_vcn.this.id
  display_name   = "sl-runner-${var.tenant_id}"

  # Egress: allow all outbound (GitHub, OCI Services).
  egress_security_rules {
    protocol    = "all"
    destination = "0.0.0.0/0"
  }

  # Ingress: deny all — runners are ephemeral and need no inbound traffic.
  # SSH is deliberately omitted. Use OCI Bastion if break-glass access needed.

  freeform_tags = local.common_tags
}

# ── Subnets ──────────────────────────────────────────────────────────────────

resource "oci_core_subnet" "private" {
  compartment_id             = var.compartment_id
  vcn_id                     = oci_core_vcn.this.id
  display_name               = "sn-private-${var.tenant_id}"
  cidr_block                 = var.private_subnet_cidr
  prohibit_public_ip_on_vnic = true
  route_table_id             = oci_core_route_table.private.id
  security_list_ids          = [oci_core_security_list.runner.id]

  freeform_tags = local.common_tags
}

resource "oci_core_subnet" "public" {
  compartment_id    = var.compartment_id
  vcn_id            = oci_core_vcn.this.id
  display_name      = "sn-public-${var.tenant_id}"
  cidr_block        = var.public_subnet_cidr
  route_table_id    = oci_core_route_table.public.id

  freeform_tags = local.common_tags
}

# ── Locals ────────────────────────────────────────────────────────────────────

locals {
  common_tags = {
    "byoc:tenant_id"  = var.tenant_id
    "byoc:managed_by" = "byoc-oci-runners"
    "byoc:module"     = "network"
  }
}
