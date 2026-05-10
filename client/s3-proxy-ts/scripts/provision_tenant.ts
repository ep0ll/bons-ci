#!/usr/bin/env tsx
/**
 * Tenant provisioning script.
 *
 * Usage:
 *   MASTER_ENC_KEY=<64-hex-chars> tsx scripts/provision_tenant.ts \
 *     --tenant-id    acme           \
 *     --name         "ACME Corp"    \
 *     --access-key   ACME000000000000000000 \
 *     --secret-key   "$(openssl rand -base64 30)" \
 *     --backend      b2             \
 *     --endpoint     s3.us-west-004.backblazeb2.com \
 *     --region       us-west-004    \
 *     --bucket       company-master-bucket \
 *     --b-access-key B2_KEY_ID      \
 *     --b-secret-key "b2-app-key"   \
 *     [--env staging]
 *
 * The script:
 *   1. Encrypts both the tenant secret key and backend secret key with
 *      AES-256-GCM using MASTER_ENC_KEY
 *   2. Writes the TenantRecord JSON to TENANT_KV via `wrangler kv:key put`
 *   3. Initialises an empty bucket list
 */

import { execSync }    from "node:child_process";
import { parseArgs }   from "node:util";
import { webcrypto }   from "node:crypto";

// ─── Parse arguments ──────────────────────────────────────────────────────────

const { values } = parseArgs({
  args:    process.argv.slice(2),
  strict:  true,
  options: {
    "tenant-id":    { type: "string" },
    "name":         { type: "string" },
    "access-key":   { type: "string" },
    "secret-key":   { type: "string" },
    "backend":      { type: "string", default: "b2" },
    "endpoint":     { type: "string" },
    "region":       { type: "string" },
    "bucket":       { type: "string" },
    "b-access-key": { type: "string" },
    "b-secret-key": { type: "string" },
    "path-style":   { type: "boolean", default: false },
    "env":          { type: "string",  default: "" },
  },
});

const required = ["tenant-id", "name", "access-key", "secret-key", "endpoint", "region", "bucket", "b-access-key", "b-secret-key"];
for (const r of required) {
  if (!values[r as keyof typeof values]) {
    console.error(`ERROR: --${r} is required`);
    process.exit(1);
  }
}

const MASTER_ENC_KEY = process.env["MASTER_ENC_KEY"];
if (!MASTER_ENC_KEY || MASTER_ENC_KEY.length !== 64) {
  console.error("ERROR: MASTER_ENC_KEY env var must be set to 64 hex chars (32 bytes)");
  process.exit(1);
}

// ─── AES-256-GCM encryption (Node.js Web Crypto) ─────────────────────────────

async function encryptValue(hexKey: string, plaintext: string): Promise<string> {
  const keyBytes = Buffer.from(hexKey, "hex");
  const key = await webcrypto.subtle.importKey(
    "raw", keyBytes, { name: "AES-GCM", length: 256 }, false, ["encrypt"],
  );

  const iv         = webcrypto.getRandomValues(new Uint8Array(12));
  const encoded    = new TextEncoder().encode(plaintext);
  const ciphertext = await webcrypto.subtle.encrypt({ name: "AES-GCM", iv }, key, encoded);

  const blob = Buffer.concat([Buffer.from(iv), Buffer.from(ciphertext)]);
  return blob.toString("base64url");
}

// ─── Main ─────────────────────────────────────────────────────────────────────

async function main(): Promise<void> {
  const tenantId   = values["tenant-id"]!   as string;
  const name       = values["name"]!         as string;
  const accessKey  = values["access-key"]!   as string;
  const secretKey  = values["secret-key"]!   as string;
  const backend    = values["backend"]!       as string;
  const endpoint   = values["endpoint"]!     as string;
  const region     = values["region"]!       as string;
  const bucket     = values["bucket"]!       as string;
  const bAccessKey = values["b-access-key"]! as string;
  const bSecretKey = values["b-secret-key"]! as string;
  const pathStyle  = values["path-style"]    as boolean;
  const envFlag    = values["env"] ? `--env ${values["env"]}` : "";

  console.log("── Encrypting tenant secret key…");
  const tenantSecretEnc  = await encryptValue(MASTER_ENC_KEY!, secretKey);

  console.log("── Encrypting backend secret key…");
  const backendSecretEnc = await encryptValue(MASTER_ENC_KEY!, bSecretKey);

  const record = {
    tenantId,
    displayName:    name,
    secretKeyEnc:   tenantSecretEnc,
    keyPrefix:      `tenant-${tenantId}/`,
    rateLimitRps:   0,
    maxObjectBytes: 0,
    defaultBackend: {
      kind:           backend,
      endpoint,
      region,
      bucket,
      accessKeyId:    bAccessKey,
      secretKeyEnc:   backendSecretEnc,
      forcePathStyle: pathStyle,
    },
  };

  const json = JSON.stringify(record, null, 2);
  const kvKey = `tenant:creds:${accessKey}`;

  console.log(`── Writing tenant record (key: ${kvKey})…`);
  execSync(
    `echo '${json.replace(/'/g, "'\\''")}' | wrangler kv:key put --binding TENANT_KV --stdin "${kvKey}" ${envFlag}`,
    { stdio: "inherit" },
  );

  console.log("── Initialising empty bucket list…");
  execSync(
    `echo '[]' | wrangler kv:key put --binding TENANT_KV --stdin "tenant:buckets:${tenantId}" ${envFlag}`,
    { stdio: "inherit" },
  );

  console.log(`
✅ Tenant '${tenantId}' provisioned.

   Access Key ID : ${accessKey}
   Secret Key    : (as supplied — store securely)
   Key prefix    : tenant-${tenantId}/
   Backend       : ${backend} → ${endpoint}

   AWS CLI:
   aws s3 ls s3:// \\
     --endpoint-url https://s3.example.com \\
     --region us-east-1

   boto3:
   import boto3
   s3 = boto3.client("s3",
       endpoint_url="${"https://s3.example.com"}",
       aws_access_key_id="${accessKey}",
       aws_secret_access_key="<secret>",
       region_name="us-east-1")
`);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
