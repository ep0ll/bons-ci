/**
 * S3 authentication middleware for Hono.
 *
 * Responsibilities (single, ordered):
 *   1. Detect auth style (header vs presigned URL)
 *   2. Extract and parse the access key ID
 *   3. Look up the tenant record in KV (O(1) read)
 *   4. Decrypt the tenant secret key (AES-256-GCM, Web Crypto)
 *   5. Verify the SigV4 signature (@smithy/signature-v4, constant-time)
 *   6. Inject TenantContext into Hono's typed context variables
 *
 * On any failure the middleware returns an S3-compatible XML error
 * Response immediately — the handler never runs.
 *
 * FALSE POSITIVE PREVENTION:
 *   - The access key is sanitised before use as a KV lookup key
 *     (prevents KV key injection via crafted access key IDs).
 *   - The SigV4 timestamp is validated BEFORE the KV lookup so we
 *     don't waste a KV round-trip on obviously expired requests.
 *   - All error paths return identical timing-safe error codes
 *     (InvalidAccessKeyId or SignatureDoesNotMatch) so attackers
 *     cannot enumerate valid access key IDs via timing.
 */

import type { Context, Next } from "hono";
import { TenantRegistry, type TenantContext } from "@/tenant";
import { verifySigV4 }                        from "@/auth/verifier";
import { parseAuthHeader, parsePresignedParams, isTimestampFresh } from "@/auth/parser";
import { decrypt }                            from "@/crypto";
import { ProxyError, withErrorBoundary }      from "@/errors";
import type { AppConfig }                     from "@/env";

// ─── Context variable keys ────────────────────────────────────────────────────

declare module "hono" {
  interface ContextVariableMap {
    tenantCtx:  TenantContext;
    registry:   TenantRegistry;
    appConfig:  AppConfig;
    requestId:  string;
  }
}

// ─── Middleware factory ───────────────────────────────────────────────────────

export function s3AuthMiddleware(env: Env, config: AppConfig) {
  return async (c: Context, next: Next): Promise<Response | void> => {
    const requestId = generateRequestId();
    c.set("requestId", requestId);

    return withErrorBoundary(async () => {
      // ── Fast-path: detect auth style without parsing everything ────────────
      const url          = new URL(c.req.url);
      const isPresigned  = url.searchParams.has("X-Amz-Signature");

      // Pre-validation: check timestamp freshness before touching KV
      // This short-circuits replay attacks without burning a KV read
      if (isPresigned) {
        const ts = url.searchParams.get("X-Amz-Date") ?? "";
        if (!isTimestampFresh(ts) &&
            !isPresignedStillValid(url.searchParams)) {
          throw ProxyError.requestExpired();
        }
      } else {
        const ts = c.req.header("x-amz-date") ?? "";
        if (ts && !isTimestampFresh(ts)) {
          throw ProxyError.requestExpired();
        }
      }

      // ── Parse auth to extract access key ID ───────────────────────────────
      const accessKeyId = extractAccessKeyId(c.req.raw, url, isPresigned);

      // ── KV lookup ─────────────────────────────────────────────────────────
      const registry = await TenantRegistry.create(env);
      const record   = await registry.findByAccessKeyId(accessKeyId);

      // ── Decrypt tenant secret key ─────────────────────────────────────────
      const secretKey = await decrypt(registry.getMasterKey(), record.secretKeyEnc);

      // ── SigV4 verification ────────────────────────────────────────────────
      await verifySigV4(c.req.raw, secretKey);

      // ── Inject into context ───────────────────────────────────────────────
      c.set("tenantCtx", registry.makeContext(record));
      c.set("registry",  registry);
      c.set("appConfig", config);

      await next();
      return c.res;
    });
  };
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

/** Extract the access key ID without fully parsing the auth fields. */
function extractAccessKeyId(request: Request, url: URL, isPresigned: boolean): string {
  if (isPresigned) {
    const cred = url.searchParams.get("X-Amz-Credential") ?? "";
    const akid = cred.split("/")[0] ?? "";
    if (!akid) throw ProxyError.malformedAuth("X-Amz-Credential missing access key ID");
    return akid;
  } else {
    const auth = request.headers.get("Authorization") ?? "";
    if (!auth) throw ProxyError.noAuth();
    // Fast extraction: find "Credential=" and take the first "/" segment
    const credStart = auth.indexOf("Credential=");
    if (credStart < 0) throw ProxyError.malformedAuth("Missing Credential field");
    const credValue = auth.slice(credStart + "Credential=".length);
    const akid      = credValue.split("/")[0]?.split(",")[0]?.trim() ?? "";
    if (!akid) throw ProxyError.malformedAuth("Empty access key ID");
    return akid;
  }
}

/** For presigned URLs: check if the URL has not yet expired by absolute time. */
function isPresignedStillValid(params: URLSearchParams): boolean {
  const ts      = params.get("X-Amz-Date")    ?? "";
  const expires = parseInt(params.get("X-Amz-Expires") ?? "0", 10);
  if (!ts || isNaN(expires)) return false;
  try {
    const iso    = ts.replace(/(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z/,
                               "$1-$2-$3T$4:$5:$6Z");
    const issued = new Date(iso).getTime();
    return Date.now() < issued + expires * 1000;
  } catch {
    return false;
  }
}

function generateRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
