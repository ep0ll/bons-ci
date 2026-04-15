# Setup & Deployment Guide

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.22+ | Build the control plane binary |
| Terraform | 1.7+ | Provision OCI infrastructure |
| Docker & Docker Compose | 24+ | Local development |
| OCI CLI | 3.x | Manual secret seeding, Vault operations |
| Make | any | Build shortcuts |

---

## 1. Local Development

### Start dependencies

```bash
docker-compose up -d
```

This starts:
- MySQL 8.0 on port 3306 (`byoc` / `byoc` / `byoc`)
- A mock GitHub webhook server on port 9000 (for local testing)

### Run the control plane

```bash
export BYOC_DB_DSN="byoc:byoc@tcp(localhost:3306)/byoc?parseTime=true"
export BYOC_LOG_PRETTY=true
export BYOC_LOG_LEVEL=debug
export BYOC_HTTP_PORT=8080

go run ./cmd/server
```

### Verify

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}

curl http://localhost:8080/readyz
# {"status":"ready"}

curl http://localhost:8080/metrics | head -20
```

---

## 2. OCI Infrastructure Provisioning

### 2.1 Configure OCI credentials

For local Terraform runs, use API key auth:

```bash
# ~/.oci/config
[DEFAULT]
user=ocid1.user.oc1...<your-user-ocid>
fingerprint=<key-fingerprint>
tenancy=ocid1.tenancy.oc1..<your-tenancy-ocid>
region=us-ashburn-1
key_file=~/.oci/oci_api_key.pem
```

### 2.2 Provision the dev environment

```bash
cd infra/terraform/environments/dev

# Create a terraform.tfvars (never commit this file)
cat > terraform.tfvars <<EOF
tenancy_ocid             = "ocid1.tenancy.oc1..<your-tenancy>"
compartment_id           = "ocid1.compartment.oc1..<control-plane-compartment>"
availability_domain      = "Uocm:US-ASHBURN-AD-1"
object_storage_namespace = "<your-namespace>"
alert_email              = "ops@yourcompany.com"
EOF

terraform init
terraform plan -out=plan.out
terraform apply plan.out
```

### 2.3 Seed secrets into OCI Vault

After `terraform apply`, populate the real secret values:

```bash
# Encode your GitHub App private key (RSA PEM)
B64_KEY=$(base64 -w0 /path/to/github-app-private-key.pem)

# Create the secret in OCI Vault
oci vault secret create-base64 \
  --compartment-id <compartment-ocid> \
  --vault-id <vault-ocid>      \       # from terraform output vault_id
  --key-id <master-key-ocid>   \       # from terraform output master_key_id
  --secret-name "github-app-key-<tenant-id>" \
  --secret-content-content "$B64_KEY"

# Seed the DB password
B64_PASS=$(echo -n "YourStrongPassword123!" | base64)
oci vault secret update-secret-content \
  --secret-id <db-password-secret-id>  \   # from terraform output db_password_secret_id
  --secret-content-content "$B64_PASS"
```

---

## 3. Building the Container Image

```bash
# Build
docker build -t byoc-oci-runners:latest .

# Tag and push to OCI Container Registry
docker tag byoc-oci-runners:latest <region>.ocir.io/<namespace>/byoc-oci-runners:latest
docker push <region>.ocir.io/<namespace>/byoc-oci-runners:latest
```

### Dockerfile (production multi-stage)

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /byoc-server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /byoc-server /byoc-server
EXPOSE 8080
ENTRYPOINT ["/byoc-server"]
```

---

## 4. Deploying to OCI Container Instances (control plane)

```bash
oci container-instances container-instance create \
  --compartment-id <compartment-ocid>              \
  --display-name byoc-control-plane                \
  --availability-domain "Uocm:US-ASHBURN-AD-1"    \
  --shape CI.Standard.E4.Flex                      \
  --shape-config '{"ocpus":2,"memoryInGBs":8}'    \
  --vnics '[{"subnetId":"<private-subnet-ocid>"}]' \
  --containers '[{
    "displayName": "byoc-server",
    "imageUrl":    "<region>.ocir.io/<ns>/byoc-oci-runners:latest",
    "environmentVariables": {
      "BYOC_DB_DSN":       "<db-endpoint>:3306/byoc?parseTime=true",
      "BYOC_LOG_LEVEL":    "info",
      "BYOC_HTTP_PORT":    "8080"
    }
  }]'
```

---

## 5. Environment Variables Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `BYOC_HTTP_PORT` | `8080` | HTTP listen port |
| `BYOC_LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `BYOC_LOG_PRETTY` | `false` | Human-readable logs (dev only) |
| `BYOC_DB_DSN` | — | MySQL DSN (`user:pass@tcp(host:port)/db?parseTime=true`) |
| `BYOC_DEBUG` | `false` | Enable Gin debug mode |
| `BYOC_VAULT_ID` | — | OCI Vault OCID for secret resolution |
| `BYOC_OCI_REGION` | — | OCI region (e.g. `us-ashburn-1`) |

---

## 6. GitHub App Setup

1. Go to **GitHub → Settings → Developer settings → GitHub Apps → New GitHub App**.
2. Set:
   - **Webhook URL**: `https://<your-control-plane>/webhooks/github/<tenant-id>`
   - **Webhook secret**: A 32-byte random string (save it — you'll store it in Vault)
   - **Permissions**:
     - Repository: Actions → Read & Write
     - Organization: Self-hosted runners → Read & Write
   - **Subscribe to events**: `workflow_job`
3. Generate a **Private Key** (PEM) and download it.
4. Note the **App ID** and **Installation ID**.
5. Store the private key in OCI Vault (see §2.3).
6. Create the tenant via the API (see tenant-onboarding.md).
