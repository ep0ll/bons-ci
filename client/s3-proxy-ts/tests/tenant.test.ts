import { describe, it, expect, beforeAll } from "vitest";
import { makeTenantContext, validateBucketName, validateObjectKey, type TenantRecord, type BackendConfig } from "../src/tenant";
import { importMasterKey, encrypt } from "../src/crypto";
import { ProxyError } from "../src/errors";

// ─── Fixtures ─────────────────────────────────────────────────────────────────

async function makeRecord(overrides: Partial<TenantRecord> = {}): Promise<{ record: TenantRecord; key: CryptoKey }> {
  const key    = await importMasterKey("1".repeat(64));
  const secretEnc  = await encrypt(key, "tenant-secret-key");
  const backendEnc = await encrypt(key, "backend-secret-key");

  const backend: BackendConfig = {
    kind: "b2", endpoint: "s3.us-west-004.backblazeb2.com",
    region: "us-west-004", bucket: "company-bucket",
    accessKeyId: "B2KEYID", secretKeyEnc: backendEnc, forcePathStyle: false,
  };

  const record: TenantRecord = {
    tenantId: "alice", displayName: "Alice Corp",
    secretKeyEnc: secretEnc, defaultBackend: backend,
    keyPrefix: "tenant-alice/",
    rateLimitRps: 0, maxObjectBytes: 0,
    createdAt: new Date().toISOString(),
    ...overrides,
  };
  return { record, key };
}

// ─── TenantContext.realKey ────────────────────────────────────────────────────

describe("TenantContext.realKey", () => {
  it("prepends tenant prefix to bucket and key", async () => {
    const { record, key } = await makeRecord();
    const ctx = makeTenantContext(record, key);
    expect(ctx.realKey("photos", "sunset.jpg")).toBe("tenant-alice/photos/sunset.jpg");
  });

  it("handles nested keys", async () => {
    const { record, key } = await makeRecord();
    const ctx = makeTenantContext(record, key);
    expect(ctx.realKey("docs", "2024/q1/report.pdf")).toBe("tenant-alice/docs/2024/q1/report.pdf");
  });

  it("two tenants with same virtual bucket produce different real keys", async () => {
    const { record: r1, key } = await makeRecord({ tenantId: "alice", keyPrefix: "tenant-alice/" });
    const { record: r2 }      = await makeRecord({ tenantId: "bob",   keyPrefix: "tenant-bob/"   });
    const ctx1 = makeTenantContext(r1, key);
    const ctx2 = makeTenantContext(r2, key);
    expect(ctx1.realKey("photos", "a.jpg")).not.toBe(ctx2.realKey("photos", "a.jpg"));
    expect(ctx1.realKey("photos", "a.jpg")).toMatch(/^tenant-alice\//);
    expect(ctx2.realKey("photos", "a.jpg")).toMatch(/^tenant-bob\//);
  });
});

// ─── TenantContext.bucketPrefix ───────────────────────────────────────────────

describe("TenantContext.bucketPrefix", () => {
  it("always ends with /", async () => {
    const { record, key } = await makeRecord();
    const ctx = makeTenantContext(record, key);
    expect(ctx.bucketPrefix("photos")).toBe("tenant-alice/photos/");
    expect(ctx.bucketPrefix("photos").endsWith("/")).toBe(true);
  });
});

// ─── TenantContext.stripPrefix ────────────────────────────────────────────────

describe("TenantContext.stripPrefix", () => {
  it("recovers virtual key from real key", async () => {
    const { record, key } = await makeRecord();
    const ctx     = makeTenantContext(record, key);
    const realKey = ctx.realKey("photos", "folder/image.png");
    expect(ctx.stripPrefix("photos", realKey)).toBe("folder/image.png");
  });

  it("returns null for key from a different bucket", async () => {
    const { record, key } = await makeRecord();
    const ctx    = makeTenantContext(record, key);
    const realKey = ctx.realKey("other-bucket", "file.txt");
    expect(ctx.stripPrefix("photos", realKey)).toBeNull();
  });

  it("returns null for key from a different tenant", async () => {
    const { record: r1, key } = await makeRecord({ tenantId: "alice", keyPrefix: "tenant-alice/" });
    const { record: r2 }      = await makeRecord({ tenantId: "bob",   keyPrefix: "tenant-bob/"   });
    const alice = makeTenantContext(r1, key);
    const bob   = makeTenantContext(r2, key);
    const bobKey = bob.realKey("photos", "secret.jpg");
    // Alice cannot strip Bob's key — returns null
    expect(alice.stripPrefix("photos", bobKey)).toBeNull();
  });
});

// ─── TenantContext.backendSecret ──────────────────────────────────────────────

describe("TenantContext.backendSecret", () => {
  it("decrypts backend secret correctly", async () => {
    const key    = await importMasterKey("2".repeat(64));
    const secret = "my-actual-backend-secret";
    const enc    = await encrypt(key, secret);
    const backend: BackendConfig = {
      kind: "s3", endpoint: "s3.amazonaws.com", region: "us-east-1",
      bucket: "b", accessKeyId: "AKID", secretKeyEnc: enc, forcePathStyle: false,
    };
    const { record } = await makeRecord({ defaultBackend: backend });
    const ctx = makeTenantContext({ ...record, defaultBackend: backend }, key);
    expect(await ctx.backendSecret()).toBe(secret);
  });

  it("throws when key is wrong", async () => {
    const key1 = await importMasterKey("3".repeat(64));
    const key2 = await importMasterKey("4".repeat(64));
    const enc  = await encrypt(key1, "secret");
    const backend: BackendConfig = {
      kind: "minio", endpoint: "minio:9000", region: "us-east-1",
      bucket: "b", accessKeyId: "AKID", secretKeyEnc: enc, forcePathStyle: true,
    };
    const { record } = await makeRecord({ defaultBackend: backend });
    const ctx = makeTenantContext({ ...record, defaultBackend: backend }, key2);
    await expect(ctx.backendSecret()).rejects.toThrow();
  });
});

// ─── validateBucketName ───────────────────────────────────────────────────────

describe("validateBucketName", () => {
  const valid = [
    "abc", "my-bucket", "a".repeat(63), "my-bucket-123",
    "all-lowercase-and-digits-123", "a1b2c3",
  ];
  const invalid: [string, string][] = [
    ["ab",            "too short"],
    ["a".repeat(64),  "too long"],
    ["UPPER",         "uppercase"],
    ["with space",    "space"],
    ["with.dot",      "dot"],
    ["with_under",    "underscore"],
    ["-start",        "starts with hyphen"],
    ["end-",          "ends with hyphen"],
    ["192.168.0.1",   "IP address"],
    ["xn--bucket",    "starts with xn--"],
    ["bucket-s3alias","ends with -s3alias"],
    ["",              "empty"],
  ];

  it.each(valid)("accepts: %s", (name) => {
    expect(() => validateBucketName(name)).not.toThrow();
  });

  it.each(invalid)("rejects: %s (%s)", (name) => {
    expect(() => validateBucketName(name)).toThrow(ProxyError);
  });
});

// ─── validateObjectKey ────────────────────────────────────────────────────────

describe("validateObjectKey", () => {
  it("accepts normal keys", () => {
    expect(() => validateObjectKey("file.txt")).not.toThrow();
    expect(() => validateObjectKey("deep/nested/path/file.bin")).not.toThrow();
    expect(() => validateObjectKey("key with spaces.txt")).not.toThrow();
    expect(() => validateObjectKey("unicode-日本語.txt")).not.toThrow();
  });

  it("throws on empty key", () => {
    expect(() => validateObjectKey("")).toThrow(ProxyError);
  });

  it("throws on key exceeding 1024 UTF-8 bytes", () => {
    // Each "a" is 1 byte; 1025 "a"s exceed the limit
    expect(() => validateObjectKey("a".repeat(1025))).toThrow(ProxyError);
    // Unicode: "中" is 3 bytes; 342 "中"s = 1026 bytes
    expect(() => validateObjectKey("中".repeat(342))).toThrow(ProxyError);
  });

  it("accepts key of exactly 1024 UTF-8 bytes", () => {
    expect(() => validateObjectKey("a".repeat(1024))).not.toThrow();
  });
});

// ─── Isolation: cross-tenant path traversal ───────────────────────────────────

describe("Tenant prefix isolation", () => {
  it("path traversal in key stays within tenant namespace", async () => {
    const { record, key } = await makeRecord({ tenantId: "alice", keyPrefix: "tenant-alice/" });
    const ctx = makeTenantContext(record, key);
    // Even if client sends "../tenant-bob/" the prefix is prepended, not bypassed
    const realKey = ctx.realKey("bucket", "../tenant-bob/secret.txt");
    // The real key starts with the tenant prefix — backend IAM enforces the rest
    expect(realKey.startsWith("tenant-alice/")).toBe(true);
    // stripPrefix on this crafted key should return null (doesn't cleanly strip "bucket/")
    // or the literal "../tenant-bob/secret.txt" string — neither is the actual bob key
    const stripped = ctx.stripPrefix("bucket", realKey);
    if (stripped !== null) {
      expect(stripped).toBe("../tenant-bob/secret.txt");
      // It's the raw string with "../" — not the resolved bob key
      expect(stripped.startsWith("tenant-bob/")).toBe(false);
    }
  });
});
