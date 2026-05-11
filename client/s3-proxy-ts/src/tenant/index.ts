/**
 * Tenant domain model, Zod schemas, and KV-backed registry.
 *
 * All KV reads are validated with Zod at the boundary so type assertions are
 * safe everywhere else. The registry is the single source of truth for:
 *   - Tenant credential lookup
 *   - Virtual bucket CRUD
 *   - Tenant provisioning
 *
 * KV key scheme:
 *   "tenant:creds:{accessKeyId}"   → TenantRecord (JSON)
 *   "tenant:buckets:{tenantId}"    → VirtualBucket[] (JSON)
 */

import { z }                  from "zod";
import { encrypt, decrypt, importMasterKey } from "@/crypto";
import { ProxyError }         from "@/errors";

// ─── Zod schemas ──────────────────────────────────────────────────────────────

export const BackendKindSchema = z.enum(["b2", "s3", "minio", "r2"]);
export type BackendKind = z.infer<typeof BackendKindSchema>;

export const BackendConfigSchema = z.object({
  kind:           BackendKindSchema,
  /**
   * S3-compatible endpoint hostname (no protocol, no trailing slash).
   * B2:    s3.us-west-004.backblazeb2.com
   * S3:    s3.amazonaws.com  (or  s3.{region}.amazonaws.com)
   * MinIO: minio.internal:9000
   * R2:    {accountId}.r2.cloudflarestorage.com
   */
  endpoint:       z.string().min(1),
  /** S3 signing region. B2: "us-west-004", S3: "us-east-1", MinIO: any. */
  region:         z.string().min(1),
  /** Real backend bucket name. */
  bucket:         z.string().min(1).max(63),
  accessKeyId:    z.string().min(1),
  /** AES-256-GCM encrypted secret key (base64url). Never plaintext. */
  secretKeyEnc:   z.string().min(1),
  /** Required for MinIO and R2; optional for B2/S3. */
  forcePathStyle: z.boolean().default(false),
  /** Optional custom endpoint URL (overrides the constructed one). */
  endpointUrl:    z.string().url().optional(),
});
export type BackendConfig = z.infer<typeof BackendConfigSchema>;

export const VirtualBucketSchema = z.object({
  name:      z.string().min(3).max(63),
  createdAt: z.string().datetime({ offset: true }),
  /** Optional per-bucket backend override (inherits tenant default if absent). */
  backend:   BackendConfigSchema.optional(),
});
export type VirtualBucket = z.infer<typeof VirtualBucketSchema>;

export const TenantRecordSchema = z.object({
  tenantId:       z.string().min(1).max(64).regex(/^[a-z0-9-]+$/),
  displayName:    z.string().min(1).max(256),
  /** Tenant's S3 secret key — AES-256-GCM encrypted. */
  secretKeyEnc:   z.string().min(1),
  defaultBackend: BackendConfigSchema,
  /**
   * Key prefix in the backend bucket. Always ends with "/".
   * Example: "tenant-alice/"
   */
  keyPrefix:      z.string().regex(/^[^/].+\/$/, "keyPrefix must not start with / and must end with /"),
  rateLimitRps:   z.number().int().nonnegative().default(0),
  maxObjectBytes: z.number().nonnegative().default(0),
  /** ISO-8601 timestamp this tenant record was created. */
  createdAt:      z.string().datetime({ offset: true }).optional(),
});
export type TenantRecord = z.infer<typeof TenantRecordSchema>;

const BucketListSchema = z.array(VirtualBucketSchema);

// ─── TenantContext ────────────────────────────────────────────────────────────

/** Fully authenticated and hydrated tenant context. */
export interface TenantContext {
  readonly record:    TenantRecord;
  readonly masterKey: CryptoKey;
  /** Decrypt and return the plaintext backend secret key. */
  backendSecret(): Promise<string>;
  /** Real backend key for a (virtualBucket, objectKey) pair. */
  realKey(bucket: string, key: string): string;
  /** Real backend prefix scoping all objects in a virtual bucket. */
  bucketPrefix(bucket: string): string;
  /**
   * Strip the real backend prefix from a real key to recover the virtual key.
   * Returns null if the key does not belong to this (bucket) prefix — used
   * as a safety check to detect misconfiguration.
   */
  stripPrefix(bucket: string, realKey: string): string | null;
  /** Effective backend config for a (possibly overridden) virtual bucket. */
  backendFor(vbucket: VirtualBucket | undefined): BackendConfig;
}

export function makeTenantContext(record: TenantRecord, masterKey: CryptoKey): TenantContext {
  return {
    record,
    masterKey,
    backendSecret: () => decrypt(masterKey, record.defaultBackend.secretKeyEnc),
    realKey:       (bucket, key)  => `${record.keyPrefix}${bucket}/${key}`,
    bucketPrefix:  (bucket)       => `${record.keyPrefix}${bucket}/`,
    stripPrefix(bucket, realKey) {
      const prefix = `${record.keyPrefix}${bucket}/`;
      return realKey.startsWith(prefix) ? realKey.slice(prefix.length) : null;
    },
    backendFor: (vbucket) => vbucket?.backend ?? record.defaultBackend,
  };
}

// ─── TenantRegistry ───────────────────────────────────────────────────────────

export class TenantRegistry {
  private constructor(
    private readonly tenantKv:  KVNamespace,
    private readonly masterKey: CryptoKey,
  ) {}

  static async create(env: Env): Promise<TenantRegistry> {
    if (!env.MASTER_ENC_KEY) throw ProxyError.internal("MASTER_ENC_KEY not set");
    const masterKey = await importMasterKey(env.MASTER_ENC_KEY);
    return new TenantRegistry(env.TENANT_KV, masterKey);
  }

  getMasterKey(): CryptoKey { return this.masterKey; }

  // ── Lookup ─────────────────────────────────────────────────────────────────

  async findByAccessKeyId(accessKeyId: string): Promise<TenantRecord> {
    // Sanitise the access key before using it as a KV key
    if (!/^[A-Za-z0-9+/=_-]{1,128}$/.test(accessKeyId)) {
      throw ProxyError.invalidKeyId();
    }

    const raw = await this.tenantKv.get(`tenant:creds:${accessKeyId}`);
    if (!raw) throw ProxyError.invalidKeyId();

    const result = TenantRecordSchema.safeParse(JSON.parse(raw));
    if (!result.success) {
      console.error("Corrupt TenantRecord:", result.error.format());
      throw ProxyError.internal("Tenant record is corrupt — contact support");
    }
    return result.data;
  }

  makeContext(record: TenantRecord): TenantContext {
    return makeTenantContext(record, this.masterKey);
  }

  // ── Virtual buckets ────────────────────────────────────────────────────────

  async listBuckets(tenantId: string): Promise<VirtualBucket[]> {
    const raw = await this.tenantKv.get(`tenant:buckets:${tenantId}`);
    if (!raw) return [];
    const result = BucketListSchema.safeParse(JSON.parse(raw));
    if (!result.success) {
      console.error("Corrupt bucket list:", result.error.format());
      return []; // graceful degradation — empty list rather than hard error
    }
    return result.data;
  }

  async findBucket(tenantId: string, name: string): Promise<VirtualBucket | null> {
    const list = await this.listBuckets(tenantId);
    return list.find((b) => b.name === name) ?? null;
  }

  async bucketExists(tenantId: string, name: string): Promise<boolean> {
    return (await this.findBucket(tenantId, name)) !== null;
  }

  async createBucket(tenantId: string, name: string): Promise<VirtualBucket> {
    validateBucketName(name);

    const buckets = await this.listBuckets(tenantId);
    if (buckets.some((b) => b.name === name)) {
      throw ProxyError.bucketAlreadyExists(name);
    }
    if (buckets.length >= 1000) {
      throw ProxyError.invalidArgument(`Tenant has reached the maximum of 1000 virtual buckets`);
    }

    const bucket: VirtualBucket = { name, createdAt: new Date().toISOString() };
    await this.tenantKv.put(
      `tenant:buckets:${tenantId}`,
      JSON.stringify([...buckets, bucket]),
    );
    return bucket;
  }

  async deleteBucket(tenantId: string, name: string): Promise<void> {
    const buckets = await this.listBuckets(tenantId);
    const filtered = buckets.filter((b) => b.name !== name);
    if (filtered.length === buckets.length) throw ProxyError.noSuchBucket(name);
    await this.tenantKv.put(`tenant:buckets:${tenantId}`, JSON.stringify(filtered));
  }

  // ── Provisioning ───────────────────────────────────────────────────────────

  async provisionTenant(params: {
    tenantId:            string;
    displayName:         string;
    accessKeyId:         string;
    secretKeyPlaintext:  string;
    backend:             Omit<BackendConfig, "secretKeyEnc"> & { secretKeyPlaintext: string };
  }): Promise<void> {
    const { tenantId, displayName, accessKeyId, secretKeyPlaintext, backend } = params;

    // Validate tenant ID before writing
    if (!/^[a-z0-9-]{1,64}$/.test(tenantId)) {
      throw ProxyError.invalidArgument("tenantId must be lowercase alphanumeric with hyphens, max 64 chars");
    }

    const secretKeyEnc   = await encrypt(this.masterKey, secretKeyPlaintext);
    const backendSecretEnc = await encrypt(this.masterKey, backend.secretKeyPlaintext);

    const record: TenantRecord = {
      tenantId,
      displayName,
      secretKeyEnc,
      defaultBackend: {
        kind:           backend.kind,
        endpoint:       backend.endpoint,
        region:         backend.region,
        bucket:         backend.bucket,
        accessKeyId:    backend.accessKeyId,
        secretKeyEnc:   backendSecretEnc,
        forcePathStyle: backend.forcePathStyle ?? false,
        endpointUrl:    backend.endpointUrl,
      },
      keyPrefix:      `tenant-${tenantId}/`,
      rateLimitRps:   0,
      maxObjectBytes: 0,
      createdAt:      new Date().toISOString(),
    };

    // Validate the complete record before writing
    const validated = TenantRecordSchema.parse(record);

    await this.tenantKv.put(`tenant:creds:${accessKeyId}`, JSON.stringify(validated));
    await this.tenantKv.put(`tenant:buckets:${tenantId}`, JSON.stringify([]));
  }
}

// ─── Bucket name validation ───────────────────────────────────────────────────

export function validateBucketName(name: string): void {
  if (name.length < 3 || name.length > 63) {
    throw ProxyError.invalidBucketName(
      `Bucket name must be 3-63 characters, got ${name.length}`
    );
  }
  if (!/^[a-z0-9][a-z0-9\-]*[a-z0-9]$/.test(name)) {
    throw ProxyError.invalidBucketName(
      "Bucket name must start and end with a lowercase letter or digit, " +
      "and contain only lowercase letters, digits, and hyphens"
    );
  }
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(name)) {
    throw ProxyError.invalidBucketName("Bucket name must not be formatted as an IP address");
  }
  if (name.startsWith("xn--")) {
    throw ProxyError.invalidBucketName("Bucket name must not start with 'xn--'");
  }
  if (name.endsWith("-s3alias")) {
    throw ProxyError.invalidBucketName("Bucket name must not end with '-s3alias'");
  }
}

/** Validate an S3 object key. */
export function validateObjectKey(key: string): void {
  if (!key) throw ProxyError.invalidArgument("Object key must not be empty");
  // S3 max key length is 1024 UTF-8 bytes
  const byteLen = new TextEncoder().encode(key).length;
  if (byteLen > 1024) {
    throw ProxyError.keyTooLong(`Key is ${byteLen} bytes; maximum is 1024`);
  }
}
