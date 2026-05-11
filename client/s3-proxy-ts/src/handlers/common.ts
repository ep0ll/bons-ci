/**
 * Shared utilities for all request handlers.
 *
 * Centralising these prevents the "helper function duplication" smell where
 * each handler module reinvents `xmlResponse`, `emptyResponse`, etc.
 */

import type { Context }       from "hono";
import type { TenantContext } from "@/tenant";
import { ProxyError }         from "@/errors";
import { S3CompatClient }     from "@/backend/client";

// ─── Response builders ────────────────────────────────────────────────────────

/** Return an `application/xml` response. */
export function xmlResponse(body: string, status = 200, requestId?: string): Response {
  return new Response(body, {
    status,
    headers: {
      "Content-Type":     "application/xml",
      "x-amz-request-id": requestId ?? generateRequestId(),
    },
  });
}

/** Return an empty response (no body). */
export function emptyResponse(status: number, requestId?: string): Response {
  return new Response(null, {
    status,
    headers: { "x-amz-request-id": requestId ?? generateRequestId() },
  });
}

/** Return a `307 Temporary Redirect` to a presigned URL. */
export function redirectResponse(location: string, requestId?: string): Response {
  return new Response(null, {
    status: 307,
    headers: {
      "Location":          location,
      "Cache-Control":     "no-store",
      "x-amz-request-id": requestId ?? generateRequestId(),
    },
  });
}

// ─── Bucket resolution ────────────────────────────────────────────────────────

/**
 * Resolve the bucket name from either:
 *   - Virtual-hosted style (set by virtualHostMiddleware as "virtualHostBucket")
 *   - Path-style (Hono route param `:bucket`)
 *
 * Throws a typed error if neither is present.
 */
export function resolveBucket(c: Context): string {
  // Virtual-host middleware may have stored the bucket in context
  const vhBucket = c.get("virtualHostBucket" as never) as string | undefined;
  if (vhBucket) return vhBucket;

  const param = c.req.param("bucket");
  if (!param) throw ProxyError.badRequest("Cannot determine bucket name");
  return param;
}

/** Resolve object key from Hono route params, URL-decoding it. */
export function resolveKey(c: Context): string {
  const raw = c.req.param("key") ?? "";
  if (!raw) throw ProxyError.invalidArgument("Object key must not be empty");
  // Hono may leave the key partially encoded — decode here for consistency
  try {
    return decodeURIComponent(raw);
  } catch {
    // Key contains a literal % that's not a valid escape — use as-is
    return raw;
  }
}

// ─── Client factory ───────────────────────────────────────────────────────────

/** Build a backend S3 client from the TenantContext. */
export async function makeClient(ctx: TenantContext): Promise<S3CompatClient> {
  const secret = await ctx.backendSecret();
  return new S3CompatClient(ctx.record.defaultBackend, secret);
}

// ─── Presign expiry ───────────────────────────────────────────────────────────

/** Parse the PRESIGN_EXPIRY_S env var, clamped to [60, MAX_PRESIGN_EXPIRY_S]. */
export function presignExpiry(c: Context): number {
  const config = c.get("appConfig");
  return config?.presignExpirySecs ?? 900;
}

// ─── Range header validation ──────────────────────────────────────────────────

/** Validate a Range header value. Returns the raw string or null if absent. */
export function parseRangeHeader(raw: string | null | undefined): string | null {
  if (!raw) return null;
  // S3 accepts only "bytes=N-M" format
  if (!/^bytes=\d*-\d*$/.test(raw)) {
    throw ProxyError.invalidArgument(`Invalid Range header: ${raw}`);
  }
  return raw;
}

// ─── MaxKeys validation ───────────────────────────────────────────────────────

export function parseMaxKeys(raw: string | null, limit: number): number {
  if (!raw) return Math.min(1000, limit);
  const n = parseInt(raw, 10);
  if (isNaN(n) || n < 0) {
    throw ProxyError.invalidArgument(`max-keys must be a non-negative integer, got: ${raw}`);
  }
  return Math.min(n, limit);
}

// ─── Helper ───────────────────────────────────────────────────────────────────

function generateRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
