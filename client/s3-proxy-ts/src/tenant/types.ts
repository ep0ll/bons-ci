/**
 * Tenant domain model.
 *
 * All shapes are validated with Zod at KV read time so runtime surprises
 * are caught immediately with a typed error rather than a downstream crash.
 */
import { z } from "zod";

// ─── Backend config ───────────────────────────────────────────────────────────

export const BackendKindSchema = z.enum(["b2", "s3", "minio"]);
export type BackendKind = z.infer<typeof BackendKindSchema>;

export const BackendConfigSchema = z.object({
  /** b2 | s3 | minio */
  kind: BackendKindSchema,
  /**
   * S3-compatible endpoint hostname (no protocol, no trailing slash).
   * B2:    s3.us-west-004.backblazeb2.com
   * S3:    s3.amazonaws.com
   * MinIO: minio.internal.example.com:9000
   */
  endpoint: z.string().min(1),
  /**
   * S3 signing region.
   * B2:    e.g. "us-west-004"
   * S3:    e.g. "us-east-1"
   * MinIO: e.g. "us-east-1" (any string MinIO accepts)
   */
  region: z.string().min(1),
  /** Real backend bucket name. */
  bucket: z.string().min(1),
  /** Backend access key ID (plaintext). */
  accessKeyId: z.string().min(1),
  /**
   * Backend secret key — AES-256-GCM encrypted, base64url-encoded.
   * Never stored in plaintext.
   */
  secretKeyEnc: z.string().min(1),
  /**
   * Whether to use path-style addressing.
   * Required for MinIO and some S3-compatible services.
   */
  forcePathStyle: z.boolean().default(false),
});
export type BackendConfig = z.infer<typeof BackendConfigSchema>;

// ─── Virtual bucket ───────────────────────────────────────────────────────────

export const VirtualBucketSchema = z.object({
  name: z.string().min(3).max(63),
  createdAt: z.string(), // ISO-8601
});
export type VirtualBucket = z.infer<typeof VirtualBucketSchema>;

// ─── Tenant record (stored in KV under "tenant:creds:{accessKeyId}") ─────────

export const TenantRecordSchema = z.object({
  tenantId: z.string().min(1),
  displayName: z.string().min(1),
  /**
   * Tenant's S3 secret key — encrypted with MASTER_ENC_KEY.
   * Used to verify incoming SigV4 signatures.
   */
  secretKeyEnc: z.string().min(1),
  defaultBackend: BackendConfigSchema,
  /**
   * Key prefix within the backend bucket that isolates this tenant.
   * Format: "tenant-{tenantId}/" — always includes trailing slash.
   */
  keyPrefix: z.string().endsWith("/"),
  /** Requests/second limit (0 = unlimited). */
  rateLimitRps: z.number().int().nonnegative().default(0),
  /** Max object size in bytes (0 = unlimited). */
  maxObjectBytes: z.number().nonnegative().default(0),
});
export type TenantRecord = z.infer<typeof TenantRecordSchema>;

// ─── Authenticated request context ───────────────────────────────────────────

/**
 * Everything a handler needs — assembled after SigV4 is verified.
 * Passed as a Hono context variable.
 */
export interface TenantContext {
  readonly record: TenantRecord;
  readonly masterKey: CryptoKey;
  /** Decrypt and return the plaintext backend secret key. */
  backendSecret(): Promise<string>;
  /** Compute the real backend key for a (virtualBucket, objectKey) pair. */
  realKey(bucket: string, key: string): string;
  /** Prefix that scopes all objects in a virtual bucket on the backend. */
  bucketPrefix(bucket: string): string;
}

// ─── Factory ─────────────────────────────────────────────────────────────────

import { decrypt } from "@/crypto";

export function makeTenantContext(
  record: TenantRecord,
  masterKey: CryptoKey
): TenantContext {
  return {
    record,
    masterKey,
    backendSecret: () => decrypt(masterKey, record.defaultBackend.secretKeyEnc),
    realKey: (bucket, key) => `${record.keyPrefix}${bucket}/${key}`,
    bucketPrefix: (bucket) => `${record.keyPrefix}${bucket}/`,
  };
}
