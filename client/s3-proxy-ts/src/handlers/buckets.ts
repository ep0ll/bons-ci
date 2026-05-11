/**
 * Bucket-level S3 operation handlers.
 *
 * All bucket operations are metadata-only (KV reads/writes).
 * The only exception is DeleteBucket, which must verify the bucket is
 * empty by listing one object from the backend before deleting.
 *
 * IDEMPOTENCY:
 *   - CreateBucket: if the bucket already exists AND is owned by this
 *     tenant, return BucketAlreadyOwnedByYou (409) — not an error in
 *     the client SDK; it's handled gracefully. Per AWS spec, creating
 *     a bucket that already exists with the same owner is a no-op in
 *     us-east-1 but an error elsewhere. We always return the AWS spec
 *     error so SDKs handle it correctly.
 */

import type { Context }  from "hono";
import {
  xmlListBuckets,
  xmlResponse,
  emptyResponse,
  resolveBucket,
  makeClient,
} from "./common";
// Re-export non-clashing names
export { xmlResponse, emptyResponse };
import { xmlListBuckets as _xmlListBuckets } from "@/protocol/xml";
import { ProxyError }   from "@/errors";

// ── GET / — ListBuckets ───────────────────────────────────────────────────────

export async function handleListBuckets(c: Context): Promise<Response> {
  const ctx      = c.get("tenantCtx");
  const registry = c.get("registry");
  const reqId    = c.get("requestId");

  const buckets  = await registry.listBuckets(ctx.record.tenantId);
  const body     = _xmlListBuckets(
    ctx.record.tenantId,
    ctx.record.displayName,
    buckets.map((b) => ({ name: b.name, createdAt: b.createdAt })),
  );
  return xmlResponse(body, 200, reqId);
}

// ── HEAD /{bucket} ────────────────────────────────────────────────────────────

export async function handleHeadBucket(c: Context): Promise<Response> {
  const ctx      = c.get("tenantCtx");
  const registry = c.get("registry");
  const reqId    = c.get("requestId");
  const bucket   = resolveBucket(c);

  const exists = await registry.bucketExists(ctx.record.tenantId, bucket);
  if (!exists) throw ProxyError.noSuchBucket(bucket);

  return emptyResponse(200, reqId);
}

// ── PUT /{bucket} — CreateBucket ──────────────────────────────────────────────

export async function handleCreateBucket(c: Context): Promise<Response> {
  const ctx      = c.get("tenantCtx");
  const registry = c.get("registry");
  const reqId    = c.get("requestId");
  const bucket   = resolveBucket(c);

  // Ignore the CreateBucketConfiguration XML body — the proxy is region-less.
  // Log it for observability but do not fail on unexpected content.
  const body = await c.req.text().catch(() => "");
  if (body && !body.includes("CreateBucketConfiguration")) {
    console.debug("CreateBucket body (unexpected):", body.slice(0, 256));
  }

  try {
    await registry.createBucket(ctx.record.tenantId, bucket);
  } catch (err) {
    if (err instanceof ProxyError && err.kind === "BucketAlreadyExists") {
      // Already owned by this tenant — return BucketAlreadyOwnedByYou
      throw ProxyError.bucketAlreadyOwnedByYou(bucket);
    }
    throw err;
  }

  return new Response(null, {
    status: 200,
    headers: {
      "Location":          `/${bucket}`,
      "x-amz-request-id": reqId ?? "",
    },
  });
}

// ── DELETE /{bucket} — DeleteBucket ───────────────────────────────────────────

export async function handleDeleteBucket(c: Context): Promise<Response> {
  const ctx      = c.get("tenantCtx");
  const registry = c.get("registry");
  const reqId    = c.get("requestId");
  const bucket   = resolveBucket(c);

  // Step 1: Verify bucket belongs to this tenant
  const exists = await registry.bucketExists(ctx.record.tenantId, bucket);
  if (!exists) throw ProxyError.noSuchBucket(bucket);

  // Step 2: Check emptiness — list exactly 1 object under the bucket prefix
  // We use maxKeys=1 to minimise backend cost
  const client   = await makeClient(ctx);
  const listing  = await client.listObjectsV2({
    prefix:  ctx.bucketPrefix(bucket),
    maxKeys: 1,
  });

  if (listing.objects.length > 0 || listing.commonPrefixes.length > 0) {
    throw ProxyError.bucketNotEmpty(bucket);
  }

  // Step 3: Remove from KV
  await registry.deleteBucket(ctx.record.tenantId, bucket);

  return emptyResponse(204, reqId);
}
