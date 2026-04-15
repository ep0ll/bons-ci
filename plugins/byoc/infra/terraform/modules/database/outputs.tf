output "db_endpoint" {
  description = "MySQL endpoint hostname."
  value       = oci_mysql_mysql_db_system.control_plane.endpoints[0].hostname
}

output "db_port" {
  description = "MySQL port."
  value       = oci_mysql_mysql_db_system.control_plane.endpoints[0].port
}

output "db_system_id" {
  description = "OCID of the MySQL DB system."
  value       = oci_mysql_mysql_db_system.control_plane.id
}
