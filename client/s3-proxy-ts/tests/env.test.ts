import { describe, it, expect } from "vitest";
import { parseEnv } from "../src/env";
import { ProxyError } from "../src/errors";

function makeEnv(overrides: Partial<Env> = {}): Env {
  return {
    TENANT_KV:           {} as KVNamespace,
    UPLOAD_KV:           {} as KVNamespace,
    MASTER_ENC_KEY:      "a".repeat(64),
    PROXY_DOMAIN:        "s3.example.com",
    LOG_LEVEL:           "info",
    PRESIGN_EXPIRY_S:    "900",
    MAX_PRESIGN_EXPIRY_S:"604800",
    UPLOAD_TTL_S:        "604800",
    MAX_KEYS_LIMIT:      "1000",
    ...overrides,
  } as Env;
}

describe("parseEnv", () => {
  it("parses a valid environment", () => {
    const config = parseEnv(makeEnv());
    expect(config.proxyDomain).toBe("s3.example.com");
    expect(config.logLevel).toBe("info");
    expect(config.presignExpirySecs).toBe(900);
    expect(config.maxPresignSecs).toBe(604800);
    expect(config.uploadTtlSecs).toBe(604800);
    expect(config.maxKeysLimit).toBe(1000);
  });

  it("trims whitespace from PROXY_DOMAIN", () => {
    const config = parseEnv(makeEnv({ PROXY_DOMAIN: "  s3.example.com  " }));
    expect(config.proxyDomain).toBe("s3.example.com");
  });

  it("defaults unknown LOG_LEVEL to info", () => {
    const config = parseEnv(makeEnv({ LOG_LEVEL: "trace" }));
    expect(config.logLevel).toBe("info");
  });

  it("accepts all valid log levels", () => {
    for (const level of ["debug", "info", "warn", "error"] as const) {
      expect(parseEnv(makeEnv({ LOG_LEVEL: level })).logLevel).toBe(level);
    }
  });

  it("throws when TENANT_KV is missing", () => {
    const env = makeEnv();
    // @ts-expect-error — intentional
    delete env.TENANT_KV;
    expect(() => parseEnv(env)).toThrow(ProxyError);
  });

  it("throws when MASTER_ENC_KEY is missing", () => {
    expect(() => parseEnv(makeEnv({ MASTER_ENC_KEY: "" }))).toThrow(ProxyError);
  });

  it("throws when PROXY_DOMAIN is missing", () => {
    expect(() => parseEnv(makeEnv({ PROXY_DOMAIN: "" }))).toThrow(ProxyError);
  });

  it("throws when PRESIGN_EXPIRY_S is non-numeric", () => {
    expect(() => parseEnv(makeEnv({ PRESIGN_EXPIRY_S: "abc" }))).toThrow(ProxyError);
  });

  it("throws when PRESIGN_EXPIRY_S is below minimum (60)", () => {
    expect(() => parseEnv(makeEnv({ PRESIGN_EXPIRY_S: "59" }))).toThrow(ProxyError);
  });

  it("throws when PRESIGN_EXPIRY_S exceeds maximum (604800)", () => {
    expect(() => parseEnv(makeEnv({ PRESIGN_EXPIRY_S: "604801" }))).toThrow(ProxyError);
  });

  it("throws when MAX_KEYS_LIMIT is 0", () => {
    expect(() => parseEnv(makeEnv({ MAX_KEYS_LIMIT: "0" }))).toThrow(ProxyError);
  });

  it("throws when MAX_KEYS_LIMIT exceeds 1000", () => {
    expect(() => parseEnv(makeEnv({ MAX_KEYS_LIMIT: "1001" }))).toThrow(ProxyError);
  });

  it("accepts boundary values for numeric fields", () => {
    expect(() => parseEnv(makeEnv({ PRESIGN_EXPIRY_S: "60" }))).not.toThrow();
    expect(() => parseEnv(makeEnv({ PRESIGN_EXPIRY_S: "604800" }))).not.toThrow();
    expect(() => parseEnv(makeEnv({ MAX_KEYS_LIMIT: "1" }))).not.toThrow();
    expect(() => parseEnv(makeEnv({ MAX_KEYS_LIMIT: "1000" }))).not.toThrow();
  });
});
