# modules/monitoring/main.tf
# Configures OCI Monitoring alarms for key BYOC platform signals.
# Prometheus metrics are scraped by OCI Monitoring Agent; alarms fire
# to a notification topic (email / PagerDuty via ONS connector).

terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = ">= 5.0"
    }
  }
}

# ── Notification Topic ────────────────────────────────────────────────────────

resource "oci_ons_notification_topic" "alerts" {
  compartment_id = var.compartment_id
  name           = "byoc-alerts-${var.environment}"
  description    = "BYOC platform operational alerts"

  freeform_tags = local.common_tags
}

resource "oci_ons_subscription" "email" {
  count          = var.alert_email != "" ? 1 : 0
  compartment_id = var.compartment_id
  topic_id       = oci_ons_notification_topic.alerts.id
  protocol       = "EMAIL"
  endpoint       = var.alert_email
}

# ── Alarm: high runner provision latency ─────────────────────────────────────

resource "oci_monitoring_alarm" "provision_latency" {
  compartment_id        = var.compartment_id
  display_name          = "byoc-high-provision-latency-${var.environment}"
  metric_compartment_id = var.compartment_id
  namespace             = "custom_byoc"
  query                 = "byoc_provision_latency_seconds[5m].p90 > 90"
  severity              = "WARNING"
  destinations          = [oci_ons_notification_topic.alerts.id]
  is_enabled            = true

  body = "BYOC runner provision latency p90 exceeded 90 seconds. Investigate OCI Compute capacity or GitHub API latency."

  freeform_tags = local.common_tags
}

# ── Alarm: runner limit saturation ────────────────────────────────────────────

resource "oci_monitoring_alarm" "queue_depth" {
  compartment_id        = var.compartment_id
  display_name          = "byoc-high-queue-depth-${var.environment}"
  metric_compartment_id = var.compartment_id
  namespace             = "custom_byoc"
  query                 = "byoc_job_queue_depth[5m].sum() > 50"
  severity              = "CRITICAL"
  destinations          = [oci_ons_notification_topic.alerts.id]
  is_enabled            = true

  body = "BYOC job queue depth exceeded 50. Tenants may be hitting runner limits. Consider raising MaxRunners or investigating stuck runners."

  freeform_tags = local.common_tags
}

# ── Alarm: control plane HTTP error rate ─────────────────────────────────────

resource "oci_monitoring_alarm" "http_error_rate" {
  compartment_id        = var.compartment_id
  display_name          = "byoc-http-error-rate-${var.environment}"
  metric_compartment_id = var.compartment_id
  namespace             = "custom_byoc"
  query                 = "byoc_api_request_duration_seconds_count{status_code='Internal Server Error'}[5m].rate() > 0.1"
  severity              = "WARNING"
  destinations          = [oci_ons_notification_topic.alerts.id]
  is_enabled            = true

  body = "BYOC API HTTP 500 error rate exceeded 10% over the last 5 minutes."

  freeform_tags = local.common_tags
}

locals {
  common_tags = {
    "byoc:managed_by" = "byoc-oci-runners"
    "byoc:module"     = "monitoring"
    "byoc:env"        = var.environment
  }
}
