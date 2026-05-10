# S3 Proxy v3 (TypeScript) — Architecture & Operations

## Overview

Multi-tenant S3-compatible proxy running on **Cloudflare Workers** (TypeScript).
Every S3 API call is authenticated, tenant-isolated, and either redirected (data
plane) or proxied (control plane) to a configurable backend: Backblaze B2, Amazon
S3, or MinIO.

```
Client (AWS SDK / s3cmd / rclone / boto3)
         │  HTTPS  AWS SigV4
         ▼
┌─────────────────────────────────────────────────────┐
│              Cloudflare Workers edge                 │
│                                                     │
│  ┌──────── Hono router ────────────────────────┐    │
│  │  Auth middleware (@smithy/signature-v4)     │    │
│  │  SigV4 verify → KV tenant lookup           │    │
│  └─────────────────────────────────────────────┘    │
│                                                     │
│  DATA PLANE (GET / PUT / UploadPart)                │
│    @aws-sdk/s3-request-presigner                    │
│    → generate presigned URL → 307 redirect          │
│    Client transfers DIRECTLY to B2/S3/MinIO         │
│                                                     │
│  CONTROL PLANE (List / Delete / Head / MPU)         │
│    @aws-sdk/client-s3 → backend API call            │
│    fast-xml-parser → parse responses                │
│    Strip tenant prefix → return to client           │
│                                                     │
│  Cloudflare KV                                      │
│    TENANT_KV:  tenant records + virtual buckets     │
│    UPLOAD_KV:  multipart upload state (7d TTL)      │
└─────────────────────────────────────────────────────┘
              │ free (B2/CF alliance or direct)
              ▼
    B2  ────  S3  ────  MinIO
```

---

## Library Rationale

Every external library was chosen to eliminate a category of hand-rolled code:

| Library | Replaces | Lines saved |
|---|---|---|
| `@smithy/signature-v4` | 300-line hand-rolled SigV4 verifier | ~280 |
| `@aws-sdk/s3-request-presigner` | 150-line hand-rolled presigned URL builder | ~130 |
| `@aws-sdk/client-s3` | 200-line HTTP client + XML assembly | ~180 |
| `fast-xml-parser` | Substring-scan XML parser | ~100 |
| `hono` | Manual URL router + middleware chain | ~80 |
| `zod` | Runtime type validation guards | ~60 |
| Web Crypto API (native) | No AES library needed | — |

**What stays hand-rolled**: XML response generation (~120 lines) — S3 response
schemas are static and well-known; a crate/library adds nothing.

---

## Presigned Redirect Architecture

```
                  Worker (< 5 ms)
Client ──GET /b/k ──────────────► verify SigV4
                                  KV lookup (tenant)
                                  realKey = prefix + b/k
                                  presignGetObject() ← @aws-sdk/s3-request-presigner
       ◄─ 307 Location: presigned ─ return redirect

Client ──GET https://s3.backblaze.com/bucket/prefix/b/k?X-Amz-Signature=... ──► B2
       ◄─ 200 + object bytes ──────────────────────────────────────────────────────
```

The Worker processes **zero bytes** of object data regardless of object size.
Memory usage is O(1) per request.

### Operations map

| Operation | Handling | Library |
|---|---|---|
| GetObject | → 307 presigned GET | `@aws-sdk/s3-request-presigner` |
| PutObject | → 307 presigned PUT | `@aws-sdk/s3-request-presigner` |
| UploadPart | → 307 presigned PUT | `@aws-sdk/s3-request-presigner` |
| HeadObject | Proxied | `@aws-sdk/client-s3` |
| DeleteObject | Proxied | `@aws-sdk/client-s3` |
| DeleteObjects | Proxied (batch SDK) | `@aws-sdk/client-s3` |
| CopyObject | Proxied (server-side) | `@aws-sdk/client-s3` |
| ListObjectsV2/V1 | Proxied + rewrite | `@aws-sdk/client-s3` |
| CreateMultipartUpload | Proxied | `@aws-sdk/client-s3` |
| CompleteMultipartUpload | Proxied | `@aws-sdk/client-s3` |
| AbortMultipartUpload | Proxied | `@aws-sdk/client-s3` |
| ListParts | Proxied | `@aws-sdk/client-s3` |
| ListBuckets | KV only | — |
| Head/Create/DeleteBucket | KV only | — |

---

## SigV4 Verification Flow

```typescript
// @smithy/signature-v4 signs the reconstructed request with the tenant's
// secret key and compares the expected signature to the client's signature.
const signer = new SignatureV4({ credentials, region, service, sha256: Sha256 });
const signed = await signer.sign(reconstructedRequest);
const expected = extractSignature(signed.headers["authorization"]);
if (!constantTimeEqual(expected, clientSignature)) throw SignatureMismatch;
```

Using `@smithy/signature-v4` means the canonical request construction,
HMAC-SHA256 key derivation, and string-to-sign computation are all handled
by the same library that powers the AWS SDK itself — impossible to diverge.

---

## Tenant Isolation

```
Backend bucket: company-master-bucket
  └── tenant-alice/
        ├── photos/sunset.jpg
        └── docs/report.pdf
  └── tenant-bob/
        └── backups/2024-01-15.tar.gz
```

- Prefix `tenant-{id}/` is prepended inside the Worker
- Clients never have backend credentials — they cannot bypass the proxy
- `@aws-sdk/client-s3` `ListObjectsV2` is called with the real prefix; results are stripped before returning to the client

---

## MinIO Support

Set `forcePathStyle: true` in the tenant backend config:

```json
{
  "kind": "minio",
  "endpoint": "minio.internal.example.com:9000",
  "region": "us-east-1",
  "bucket": "my-bucket",
  "accessKeyId": "minioadmin",
  "secretKeyEnc": "<encrypted>",
  "forcePathStyle": true
}
```

The `@aws-sdk/client-s3` `forcePathStyle` flag enables path-style addressing
(`endpoint/bucket/key` vs `bucket.endpoint/key`), which MinIO requires unless
virtual-host DNS is configured.

---

## Security

| Threat | Mitigation |
|---|---|
| Signature replay | `x-amz-date` freshness check (±15 min) in `verifySigV4` |
| Timing attack on HMAC | `constantTimeEqual` (bit-fold XOR comparison) |
| Credential exfiltration | AES-256-GCM encrypted in KV; master key is Workers Secret |
| Cross-tenant access | Prefix namespace; per-tenant credentials verified before prefixing |
| GCM tag tampering | Web Crypto `decrypt` throws on tag mismatch |
| Path traversal | `realKey()` always prepends prefix; backend enforces IAM prefix policy |

---

## Operations

### Generate master key
```bash
openssl rand -hex 32          # 64 hex chars = 32 bytes
wrangler secret put MASTER_ENC_KEY
```

### Provision a tenant (Backblaze B2)
```bash
MASTER_ENC_KEY=<64-hex> npx tsx scripts/provision_tenant.ts \
  --tenant-id    acme \
  --name         "ACME Corp" \
  --access-key   ACME00000000000000000000 \
  --secret-key   "$(openssl rand -base64 30)" \
  --backend      b2 \
  --endpoint     s3.us-west-004.backblazeb2.com \
  --region       us-west-004 \
  --bucket       company-master-bucket \
  --b-access-key B2_KEY_ID \
  --b-secret-key "b2-application-key"
```

### Provision a tenant (MinIO)
```bash
MASTER_ENC_KEY=<64-hex> npx tsx scripts/provision_tenant.ts \
  --tenant-id    dev-team \
  --name         "Dev Team" \
  --access-key   DEVTEAM000000000000000 \
  --secret-key   "$(openssl rand -base64 30)" \
  --backend      minio \
  --endpoint     minio.internal:9000 \
  --region       us-east-1 \
  --bucket       dev-bucket \
  --b-access-key minioadmin \
  --b-secret-key minioadmin123 \
  --path-style
```

### Deploy
```bash
npm run deploy               # production
npm run deploy -- --env staging
```

### Test
```bash
npm test                     # unit + integration
npm run test:coverage        # with coverage report
```

### AWS CLI client example
```bash
aws configure set aws_access_key_id     TENANT_ACCESS_KEY
aws configure set aws_secret_access_key TENANT_SECRET_KEY

# List buckets
aws s3 ls --endpoint-url https://s3.example.com

# Upload (issues PUT → gets 307 → follows to B2 directly)
aws s3 cp ./large-file.bin s3://photos/large-file.bin \
  --endpoint-url https://s3.example.com

# Multipart upload (SDK handles chunking, all parts → 307 → B2 directly)
aws s3 cp ./50gb-file.bin s3://backups/50gb-file.bin \
  --endpoint-url https://s3.example.com
```

### rclone
```ini
[proxy]
type              = s3
provider          = Other
access_key_id     = TENANT_ACCESS_KEY
secret_access_key = TENANT_SECRET_KEY
endpoint          = https://s3.example.com
force_path_style  = true
```
