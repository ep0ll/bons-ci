/**
 * Tenant registry — reads and writes tenant records from Cloudflare KV.
 *
 * KV key scheme:
 *   "tenant:creds:{accessKeyId}"   → TenantRecord (JSON, Zod-validated)
 *   "tenant:buckets:{tenantId}"    → VirtualBucket[] (JSON, Zod-validated)
 */
import { z } from "zod";
import { encrypt, importMasterKey } from "@/crypto";
import { ProxyError } from "@/errors";
import {
  BackendConfig,
  TenantRecord,
  TenantRecordSchema,
  VirtualBucket,
  VirtualBucketSchema,
  makeTenantContext,
  type TenantContext,
} from "./types";

const BucketListSchema = z.array(VirtualBucketSchema);

// ─── Registry class ───────────────────────────────────────────────────────────

export class TenantRegistry {
  private readonly kv: KVNamespace;
  private readonly masterKey: CryptoKey;

  private constructor(kv: KVNamespace, masterKey: CryptoKey) {
    this.kv = kv;
    this.masterKey = masterKey;
  }

  /** Factory — resolves the KV binding and decodes the master key. */
  static async create(env: Env): Promise<TenantRegistry> {
    const hexKey = env.MASTER_ENC_KEY;
    if (!hexKey) throw ProxyError.internal("MASTER_ENC_KEY secret is not set");

    const masterKey = await importMasterKey(hexKey).catch((e: unknown) => {
      throw ProxyError.internal(`Invalid MASTER_ENC_KEY: ${String(e)}`);
    });

    return new TenantRegistry(env.TENANT_KV, masterKey);
  }

  // ── Tenant lookup ──────────────────────────────────────────────────────────

  /** Resolve an access key ID to a validated TenantRecord. */
  async findByAccessKeyId(accessKeyId: string): Promise<TenantRecord> {
    const raw = await this.kv.get(`tenant:creds:${accessKeyId}`);
    if (!raw) throw ProxyError.invalidKeyId();

    const parsed = TenantRecordSchema.safeParse(JSON.parse(raw));
    if (!parsed.success) {
      throw ProxyError.internal(
        `Corrupt tenant record: ${parsed.error.message}`
      );
    }
    return parsed.data;
  }

  /** Build a fully hydrated TenantContext after authentication. */
  makeContext(record: TenantRecord): TenantContext {
    return makeTenantContext(record, this.masterKey);
  }

  // ── Virtual bucket operations ──────────────────────────────────────────────

  async listBuckets(tenantId: string): Promise<VirtualBucket[]> {
    const raw = await this.kv.get(`tenant:buckets:${tenantId}`);
    if (!raw) return [];
    return BucketListSchema.parse(JSON.parse(raw));
  }

  async createBucket(tenantId: string, name: string): Promise<VirtualBucket> {
    validateBucketName(name);

    const buckets = await this.listBuckets(tenantId);
    if (buckets.some((b) => b.name === name)) {
      throw ProxyError.bucketAlreadyExists(name);
    }

    const bucket: VirtualBucket = { name, createdAt: new Date().toISOString() };
    await this.kv.put(
      `tenant:buckets:${tenantId}`,
      JSON.stringify([...buckets, bucket])
    );
    return bucket;
  }

  async deleteBucket(tenantId: string, name: string): Promise<void> {
    const buckets = await this.listBuckets(tenantId);
    const filtered = buckets.filter((b) => b.name !== name);
    if (filtered.length === buckets.length) throw ProxyError.noSuchBucket(name);

    await this.kv.put(`tenant:buckets:${tenantId}`, JSON.stringify(filtered));
  }

  async bucketExists(tenantId: string, name: string): Promise<boolean> {
    const buckets = await this.listBuckets(tenantId);
    return buckets.some((b) => b.name === name);
  }

  // ── Tenant provisioning ───────────────────────────────────────────────────

  /**
   * Create a new tenant record in KV.
   * `secretKeyPlaintext` is encrypted before storage — never written raw.
   */
  async provisionTenant(params: {
    tenantId: string;
    displayName: string;
    accessKeyId: string;
    secretKeyPlaintext: string;
    backend: BackendConfig;
  }): Promise<void> {
    const { tenantId, displayName, accessKeyId, secretKeyPlaintext, backend } =
      params;

    const secretKeyEnc = await encrypt(this.masterKey, secretKeyPlaintext);
    const backendEnc = { ...backend }; // backend.secretKeyEnc is already encrypted by caller

    const record: TenantRecord = {
      tenantId,
      displayName,
      secretKeyEnc,
      defaultBackend: backendEnc,
      keyPrefix: `tenant-${tenantId}/`,
      rateLimitRps: 0,
      maxObjectBytes: 0,
    };

    await this.kv.put(`tenant:creds:${accessKeyId}`, JSON.stringify(record));
    await this.kv.put(`tenant:buckets:${tenantId}`, JSON.stringify([]));
  }

  /** Expose master key for presigned URL generation. */
  getMasterKey(): CryptoKey {
    return this.masterKey;
  }
}

// ─── Bucket name validation ───────────────────────────────────────────────────

function validateBucketName(name: string): void {
  if (name.length < 3 || name.length > 63) {
    throw ProxyError.badRequest(
      `Bucket name must be 3-63 chars, got ${name.length}`
    );
  }
  if (!/^[a-z0-9][a-z0-9-]*[a-z0-9]$/.test(name)) {
    throw ProxyError.badRequest(
      "Bucket name may only contain lowercase letters, digits, and hyphens, and must start and end with a letter or digit"
    );
  }
  if (/^\d+\.\d+\.\d+\.\d+$/.test(name)) {
    throw ProxyError.badRequest(
      "Bucket name must not be formatted as an IP address"
    );
  }
}
