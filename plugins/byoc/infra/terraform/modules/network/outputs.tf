output "vcn_id" {
  description = "OCID of the tenant VCN."
  value       = oci_core_vcn.this.id
}

output "private_subnet_id" {
  description = "OCID of the private subnet where runner instances are placed."
  value       = oci_core_subnet.private.id
}

output "public_subnet_id" {
  description = "OCID of the public subnet."
  value       = oci_core_subnet.public.id
}

output "nat_gateway_id" {
  description = "OCID of the NAT gateway."
  value       = oci_core_nat_gateway.this.id
}
