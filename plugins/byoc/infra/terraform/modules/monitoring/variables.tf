variable "compartment_id" { type = string }
variable "environment" { type = string; default = "dev" }
variable "alert_email" {
  description = "Email address for alarm notifications. Leave empty to skip email subscription."
  type        = string
  default     = ""
}
