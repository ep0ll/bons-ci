# Tenant Onboarding Guide

This guide walks through onboarding a new tenant ("Acme Corp") end-to-end.

---

## Step 1: Provision OCI Compartment & Network

Create a dedicated OCI compartment for the tenant (done by the platform ops team or
automated via the BYOC provisioning API in a future release).

```bash
# Create the compartment
oci iam compartment create \
  --compartment-id <root-compartment-ocid> \
  --name "byoc-tenant-acme-corp"           \
  --description "BYOC runners for Acme Corp"

# Note the compartment OCID from the output:
# ACME_COMPARTMENT_ID="ocid1.compartment.oc1..acme..."
```

Then apply the tenant-specific Terraform modules:

```hcl
# Add to environments/prod/main.tf (or a separate tenant workspace)

module "tenant_acme_network" {
  source              = "../../modules/network"
  compartment_id      = "ocid1.compartment.oc1..acme..."
  tenant_id           = "acme-corp"
  vcn_cidr            = "10.10.0.0/16"
  private_subnet_cidr = "10.10.1.0/24"
  public_subnet_cidr  = "10.10.0.0/24"
}

module "tenant_acme_iam" {
  source                   = "../../modules/iam"
  tenancy_ocid             = var.tenancy_ocid
  compartment_id           = "ocid1.compartment.oc1..acme..."
  tenant_id                = "acme-corp"
  object_storage_namespace = var.object_storage_namespace
}
```

```bash
terraform apply
# Outputs:
#   tenant_acme_private_subnet_id = "ocid1.subnet.oc1..acme-private..."
```

---

## Step 2: Set Up GitHub App

1. Acme Corp creates a GitHub App in their organization:
   - **Webhook URL**: `https://byoc.example.com/webhooks/github/acme-corp`
   - **Webhook Secret**: `<32-random-bytes>` — Acme generates this

2. Acme shares with the platform operator:
   - GitHub App ID: `12345`
   - GitHub Installation ID: `67890`
   - GitHub Org name: `acme-corp`
   - Webhook secret: `<32-random-bytes>`
   - Private key PEM: `<downloaded-from-github>`

---

## Step 3: Store Secrets in OCI Vault

```bash
# GitHub App private key
B64_KEY=$(base64 -w0 /path/to/acme-github-app-key.pem)
oci vault secret create-base64 \
  --compartment-id <control-plane-compartment> \
  --vault-id <vault-ocid>                      \
  --key-id   <master-key-ocid>                 \
  --secret-name "github-app-key-acme-corp"     \
  --secret-content-content "$B64_KEY"

# GitHub installation ID (stored as plain string)
oci vault secret create-base64 \
  --compartment-id <control-plane-compartment> \
  --vault-id <vault-ocid>                      \
  --key-id   <master-key-ocid>                 \
  --secret-name "github-install-id-acme-corp"  \
  --secret-content-content "$(echo -n '67890' | base64)"

# GitHub org name
oci vault secret create-base64 \
  --compartment-id <control-plane-compartment> \
  --vault-id <vault-ocid>                      \
  --key-id   <master-key-ocid>                 \
  --secret-name "github-org-acme-corp"         \
  --secret-content-content "$(echo -n 'acme-corp' | base64)"
```

---

## Step 4: Register the Tenant via the API

```bash
curl -X POST https://byoc.example.com/v1/tenants \
  -H "Content-Type: application/json"            \
  -d '{
    "name":               "Acme Corp",
    "github_app_id":      12345,
    "github_install_id":  67890,
    "github_org_name":    "acme-corp",
    "webhook_secret":     "<32-random-bytes>",
    "oci_compartment_id": "ocid1.compartment.oc1..acme...",
    "oci_subnet_id":      "ocid1.subnet.oc1..acme-private...",
    "max_runners":        50,
    "min_warm_pool":      2,
    "idle_timeout_sec":   300,
    "runner_labels":      ["acme", "ubuntu-22.04"],
    "runner_shape":       "VM.Standard.E4.Flex",
    "runner_ocpus":       2,
    "runner_memory_gb":   8,
    "provisioner_type":   "compute"
  }'
```

**Expected response (201 Created):**

```json
{
  "data": {
    "id":               "550e8400-e29b-41d4-a716-446655440000",
    "name":             "Acme Corp",
    "github_app_id":    12345,
    "github_org_name":  "acme-corp",
    "oci_compartment_id": "ocid1.compartment.oc1..acme...",
    "oci_subnet_id":    "ocid1.subnet.oc1..acme-private...",
    "max_runners":      50,
    "min_warm_pool":    2,
    "idle_timeout_sec": 300,
    "runner_labels":    ["acme", "ubuntu-22.04"],
    "runner_shape":     "VM.Standard.E4.Flex",
    "runner_ocpus":     2,
    "runner_memory_gb": 8,
    "provisioner_type": "compute",
    "status":           "active",
    "created_at":       "2026-04-16T10:00:00Z",
    "updated_at":       "2026-04-16T10:00:00Z"
  },
  "error":      null,
  "request_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
}
```

Save the returned `id` — this is the tenant ID used in the webhook URL.

---

## Step 5: Verify Runner Provisioning

Create a test GitHub Actions workflow in an Acme Corp repository:

```yaml
# .github/workflows/test-byoc-runner.yml
name: Test BYOC Runner

on: [push]

jobs:
  test:
    runs-on: [self-hosted, acme, ubuntu-22.04]
    steps:
      - name: Verify runner
        run: |
          echo "Running on: $(hostname)"
          echo "Runner labels: $RUNNER_LABELS"
          uname -a
```

Push a commit and watch:

```bash
# Poll the runners API
watch -n 5 'curl -s https://byoc.example.com/v1/tenants/550e8400.../runners | jq .'

# Or check the control plane logs
journalctl -u byoc-server -f | grep acme-corp
```

Expected sequence in logs:
```
{"action":"provision_runner","tenant_id":"550e8400...","runner_id":"...","job_id":12345}
{"action":"oci_instance_launched","oci_instance_id":"ocid1.instance..."}
{"action":"runner_state_transition","from":"provisioning","to":"registering"}
{"action":"runner_state_transition","from":"registering","to":"idle"}
{"action":"runner_state_transition","from":"idle","to":"busy"}
{"action":"job_completed","tenant_id":"550e8400...","runner_id":"..."}
{"action":"runner_terminated","reason":"job_completed"}
```

---

## Step 6: Configure Warm Pool (Optional)

If Acme Corp wants pre-provisioned idle runners to avoid cold-start latency:

```bash
# PATCH is not yet implemented in the example — use the store directly or
# extend the tenant handler with a PATCH endpoint.
# The MinWarmPool field on the tenant record controls this:
# min_warm_pool: 2 means the orchestrator will maintain 2 idle runners at all times.
```

The warm pool manager (add to orchestrator as a future enhancement) should:
1. On startup and on a 60-second tick, count idle runners per tenant.
2. If `idle_count < min_warm_pool`, provision `min_warm_pool - idle_count` new runners
   with `JobID = 0` (warm pool runners, not tied to a specific job).

---

## Offboarding a Tenant

```bash
# Marks tenant as "offboarding" — the reconciler drains running jobs
curl -X DELETE https://byoc.example.com/v1/tenants/550e8400.../
```

Then clean up OCI resources:

```bash
# Remove the tenant Terraform resources
terraform destroy -target=module.tenant_acme_network -target=module.tenant_acme_iam

# Delete the OCI compartment (only after all instances are terminated)
oci iam compartment delete --compartment-id <acme-compartment-ocid>
```
