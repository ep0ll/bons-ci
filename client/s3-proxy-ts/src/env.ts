/**
 * Typed Cloudflare Workers environment.
 *
 * `Env` is declared globally so wrangler.toml bindings are available
 * everywhere without passing env through every call frame.
 *
 * `parseEnv` validates all required fields at request start and throws
 * a typed error if any are missing or malformed — catching config bugs
 * at the edge of the runtime, not deep inside business logic.
 */

import { ProxyError } from "@/errors";

// ─── Global Env type (matches wrangler.toml) ──────────────────────────────────

declare global {
  interface Env {
    // KV namespaces
    TENANT_KV:   KVNamespace;
    UPLOAD_KV:   KVNamespace;
    // Secrets
    MASTER_ENC_KEY: string;
    // Vars
    PROXY_DOMAIN:         string;
    LOG_LEVEL:            string;
    PRESIGN_EXPIRY_S:     string;
    MAX_PRESIGN_EXPIRY_S: string;
    UPLOAD_TTL_S:         string;
    MAX_KEYS_LIMIT:       string;
  }
}

// ─── Validated, typed config ──────────────────────────────────────────────────

export interface AppConfig {
  proxyDomain:       string;
  logLevel:          "debug" | "info" | "warn" | "error";
  presignExpirySecs: number;
  maxPresignSecs:    number;
  uploadTtlSecs:     number;
  maxKeysLimit:      number;
}

export function parseEnv(env: Env): AppConfig {
  // KV bindings
  if (!env.TENANT_KV) throw ProxyError.internal("TENANT_KV binding missing");
  if (!env.UPLOAD_KV) throw ProxyError.internal("UPLOAD_KV binding missing");

  // Master key — validated on first use in TenantRegistry
  if (!env.MASTER_ENC_KEY) throw ProxyError.internal("MASTER_ENC_KEY secret not set");

  // Proxy domain
  const proxyDomain = env.PROXY_DOMAIN?.trim();
  if (!proxyDomain) throw ProxyError.internal("PROXY_DOMAIN var missing");

  // Numeric vars with range guards
  const presignExpirySecs = parsePositiveInt(env.PRESIGN_EXPIRY_S, "PRESIGN_EXPIRY_S", 60, 604800);
  const maxPresignSecs    = parsePositiveInt(env.MAX_PRESIGN_EXPIRY_S, "MAX_PRESIGN_EXPIRY_S", 60, 604800);
  const uploadTtlSecs     = parsePositiveInt(env.UPLOAD_TTL_S, "UPLOAD_TTL_S", 300, 604800);
  const maxKeysLimit      = parsePositiveInt(env.MAX_KEYS_LIMIT, "MAX_KEYS_LIMIT", 1, 1000);

  const logRaw = (env.LOG_LEVEL ?? "info").toLowerCase();
  const logLevel = (["debug", "info", "warn", "error"] as const).find((l) => l === logRaw) ?? "info";

  return { proxyDomain, logLevel, presignExpirySecs, maxPresignSecs, uploadTtlSecs, maxKeysLimit };
}

function parsePositiveInt(raw: string | undefined, name: string, min: number, max: number): number {
  const n = parseInt(raw ?? "", 10);
  if (isNaN(n) || n < min || n > max) {
    throw ProxyError.internal(`${name} must be an integer between ${min} and ${max}, got: ${raw ?? "(unset)"}`);
  }
  return n;
}
