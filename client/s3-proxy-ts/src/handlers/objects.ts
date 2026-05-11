/**
 * Object-level S3 operation handlers.
 *
 * DATA PLANE (GET, PUT):
 *   Returns a `307 Temporary Redirect` to a presigned backend URL via
 *   `@aws-sdk/s3-request-presigner`. The client's SDK follows the redirect
 *   and transfers bytes directly to/from the backend. The Worker processes
 *   zero bytes of object data regardless of object size.
 *
 * CONTROL PLANE (HEAD, DELETE, Copy, List, DeleteObjects):
 *   Proxied through the Worker via `@aws-sdk/client-s3`. Response data is
 *   rewritten (tenant prefix stripped) before returning to the client.
 *
 * KEY VALIDATION:
 *   Object keys are validated for length (≤1024 UTF-8 bytes per S3 spec)
 *   before any backend call. This prevents backend errors from leaking
 *   through as internal errors.
 *
 * ETAG HANDLING:
 *   ETags from the backend are forwarded verbatim. S3 returns ETags wrapped
 *   in double quotes (`"abc123"`); we preserve this format.
 *
 * COPY OBJECT ISOLATION:
 *   The x-amz-copy-source header is rewritten to the real backend key.
 *   Cross-tenant copies are rejected — both source and destination must
 *   resolve to the same tenant's key prefix.
 */

import type { Context }     from "hono";
import {
  resolveBucket, resolveKey, makeClient, redirectResponse,
  xmlResponse, emptyResponse, presignExpiry, parseMaxKeys,
} from "./common";
import { S3CompatClient }   from "@/backend/client";
import { presignGet, presignPut } from "@/backend/presigned";
import {
  parseDeleteRequest,
  xmlListObjectsV2,
  xmlListObjectsV1,
  xmlDeleteResult,
  xmlCopyObjectResult,
} from "@/protocol/xml";
import { ProxyError }       from "@/errors";
import { validateObjectKey } from "@/tenant";

// ── GET /{bucket}/{key} → 307 presigned redirect ──────────────────────────────

export async function handleGetObject(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);

  validateObjectKey(key);

  const client  = await makeClient(ctx);
  const realKey = ctx.realKey(bucket, key);
  const url     = await presignGet(client.rawClient, client.bucket, realKey, {
    expiresIn: presignExpiry(c),
  });

  return redirectResponse(url, reqId);
}

// ── PUT /{bucket}/{key} → 307 presigned redirect ──────────────────────────────

export async function handlePutObject(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);

  validateObjectKey(key);

  // Enforce max object size if configured
  const maxBytes   = ctx.record.maxObjectBytes;
  const contentLen = parseInt(c.req.header("content-length") ?? "0", 10);
  if (maxBytes > 0 && contentLen > maxBytes) {
    throw ProxyError.entityTooLarge(
      `Object size ${contentLen} exceeds tenant limit of ${maxBytes} bytes`,
    );
  }

  const client      = await makeClient(ctx);
  const realKey     = ctx.realKey(bucket, key);
  const contentType = c.req.header("content-type") ?? "application/octet-stream";

  // Forward user-defined metadata (x-amz-meta-*) to the presigned PUT
  const metadata   = extractUserMetadata(c.req.raw.headers);

  const url = await presignPut(client.rawClient, client.bucket, realKey, {
    expiresIn:   presignExpiry(c),
    contentType,
    metadata,
  });

  return redirectResponse(url, reqId);
}

// ── HEAD /{bucket}/{key} — proxied ────────────────────────────────────────────

export async function handleHeadObject(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);

  validateObjectKey(key);

  const client  = await makeClient(ctx);
  const realKey = ctx.realKey(bucket, key);
  const meta    = await client.headObject(realKey);

  const headers: Record<string, string> = {
    "Content-Type":     meta.contentType,
    "Content-Length":   String(meta.contentLength),
    "ETag":             meta.etag,
    "Last-Modified":    meta.lastModified.toUTCString(),
    "x-amz-request-id": reqId ?? "",
    ...meta.extraHeaders,
  };

  return new Response(null, { status: 200, headers });
}

// ── DELETE /{bucket}/{key} — proxied ─────────────────────────────────────────

export async function handleDeleteObject(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);

  validateObjectKey(key);

  const client  = await makeClient(ctx);
  const realKey = ctx.realKey(bucket, key);
  await client.deleteObject(realKey);

  return emptyResponse(204, reqId);
}

// ── POST /{bucket}?delete — DeleteObjects (batch) ─────────────────────────────

export async function handleDeleteObjects(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const config = c.get("appConfig");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);

  const body = await c.req.text();
  if (!body.trim()) throw ProxyError.malformedXML("Request body is empty");

  let parsed;
  try {
    parsed = parseDeleteRequest(body);
  } catch (e) {
    throw ProxyError.malformedXML(e instanceof Error ? e.message : String(e));
  }

  const maxKeys = config?.maxKeysLimit ?? 1000;
  if (parsed.objects.length > maxKeys) {
    throw ProxyError.invalidArgument(
      `DeleteObjects accepts at most ${maxKeys} objects per request, got ${parsed.objects.length}`,
    );
  }

  // Map virtual keys → real keys, maintaining a reverse map for the response
  const realToVirtual = new Map<string, string>();
  const realKeys: string[] = [];

  for (const obj of parsed.objects) {
    if (!obj.Key) throw ProxyError.malformedXML("Object has empty Key");
    validateObjectKey(obj.Key);
    const realKey = ctx.realKey(bucket, obj.Key);
    realKeys.push(realKey);
    realToVirtual.set(realKey, obj.Key);
  }

  const client = await makeClient(ctx);
  const result = await client.deleteObjects(realKeys);

  // Map real keys back to virtual keys in the response
  const deleted = result.deleted.map((rk) => realToVirtual.get(rk) ?? rk);
  const errors  = result.errors.map(({ key: rk, code, message }) => ({
    key:     realToVirtual.get(rk) ?? rk,
    code,
    message,
  }));

  return xmlResponse(xmlDeleteResult(deleted, errors), 200, reqId);
}

// ── PUT /{bucket}/{key} with x-amz-copy-source — CopyObject ──────────────────

export async function handleCopyObject(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);

  validateObjectKey(key);

  // Parse x-amz-copy-source: can be "/{bucket}/{key}" or "{bucket}/{key}"
  // It may also include a versionId: "{bucket}/{key}?versionId=xxx"
  const rawSource  = c.req.header("x-amz-copy-source") ?? "";
  if (!rawSource) throw ProxyError.invalidArgument("x-amz-copy-source header is required for CopyObject");

  // Strip leading slash and version ID
  const sourceNoVersion = rawSource.replace(/^\//, "").split("?")[0]!;
  const slashIdx        = sourceNoVersion.indexOf("/");
  if (slashIdx < 0) {
    throw ProxyError.invalidArgument(
      `x-amz-copy-source must be in format {bucket}/{key}, got: ${rawSource}`,
    );
  }

  const srcBucket  = decodeURIComponent(sourceNoVersion.slice(0, slashIdx));
  const srcKey     = decodeURIComponent(sourceNoVersion.slice(slashIdx + 1));

  if (!srcBucket) throw ProxyError.invalidArgument("Copy source bucket is empty");
  if (!srcKey)    throw ProxyError.invalidArgument("Copy source key is empty");

  validateObjectKey(srcKey);
  validateObjectKey(key);

  // Verify source bucket belongs to this tenant (cross-tenant copies forbidden)
  const registry  = c.get("registry");
  const srcExists = await registry.bucketExists(ctx.record.tenantId, srcBucket);
  if (!srcExists) throw ProxyError.noSuchBucket(srcBucket);

  const srcReal = ctx.realKey(srcBucket, srcKey);
  const dstReal = ctx.realKey(bucket, key);

  const metadataDirective = (c.req.header("x-amz-metadata-directive") ?? "COPY") as "COPY" | "REPLACE";

  const client = await makeClient(ctx);
  const result = await client.copyObject(srcReal, dstReal, {
    metadataDirective,
    contentType: metadataDirective === "REPLACE"
      ? c.req.header("content-type") ?? undefined
      : undefined,
    metadata: metadataDirective === "REPLACE"
      ? extractUserMetadata(c.req.raw.headers)
      : undefined,
  });

  const now = new Date().toISOString();
  return xmlResponse(xmlCopyObjectResult(result.etag, now), 200, reqId);
}

// ── GET /{bucket} — ListObjects (V1 and V2) ───────────────────────────────────

export async function handleListObjects(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const config = c.get("appConfig");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);

  const qp          = (key: string) => c.req.query(key) ?? null;
  const isV2        = qp("list-type") === "2";
  const prefix      = qp("prefix")    ?? "";
  const delimiter   = qp("delimiter") ?? "";
  const maxKeys     = parseMaxKeys(qp("max-keys"), config?.maxKeysLimit ?? 1000);
  const startAfter  = qp("start-after")        ?? undefined;
  const fetchOwner  = qp("fetch-owner") === "true";

  // Continuation token differs between V1 (marker) and V2 (continuation-token)
  const continuation = isV2
    ? qp("continuation-token") ?? undefined
    : qp("marker")              ?? undefined;

  // Verify bucket exists before hitting the backend
  const registry = c.get("registry");
  const exists   = await registry.bucketExists(ctx.record.tenantId, bucket);
  if (!exists) throw ProxyError.noSuchBucket(bucket);

  // Prepend the tenant+bucket prefix to the client's prefix
  const bucketPrefix = ctx.bucketPrefix(bucket);
  const realPrefix   = `${bucketPrefix}${prefix}`;

  const client  = await makeClient(ctx);
  const listing = await client.listObjectsV2({
    prefix:            realPrefix,
    delimiter:         delimiter || undefined,
    maxKeys,
    continuationToken: continuation,
    startAfter:        startAfter ? `${bucketPrefix}${startAfter}` : undefined,
    fetchOwner,
  });

  // Strip tenant+bucket prefix from all keys and prefixes
  const contents = listing.objects.map((obj) => {
    const virtualKey = ctx.stripPrefix(bucket, obj.key) ?? obj.key;
    return {
      key:          virtualKey,
      lastModified: obj.lastModified.toISOString(),
      etag:         obj.etag,
      size:         obj.size,
      storageClass: obj.storageClass,
    };
  });

  const commonPrefixes = listing.commonPrefixes.map(
    (cp) => ctx.stripPrefix(bucket, cp) ?? cp,
  );

  if (isV2) {
    return xmlResponse(
      xmlListObjectsV2({
        bucket,
        prefix,
        delimiter,
        maxKeys,
        isTruncated:           listing.isTruncated,
        keyCount:              contents.length,
        nextContinuationToken: listing.nextContinuationToken,
        continuationToken:     continuation,
        startAfter,
        contents,
        commonPrefixes,
      }),
      200, reqId,
    );
  }

  // ListObjects V1
  const nextMarker = listing.isTruncated
    ? contents[contents.length - 1]?.key
    : undefined;

  return xmlResponse(
    xmlListObjectsV1({
      bucket,
      prefix,
      delimiter,
      marker:      continuation ?? "",
      maxKeys,
      isTruncated: listing.isTruncated,
      nextMarker,
      contents,
      commonPrefixes,
    }),
    200, reqId,
  );
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

/** Extract x-amz-meta-* headers for forwarding to the backend. */
function extractUserMetadata(headers: Headers): Record<string, string> {
  const meta: Record<string, string> = {};
  headers.forEach((value, key) => {
    const lower = key.toLowerCase();
    if (lower.startsWith("x-amz-meta-")) {
      // Strip the "x-amz-meta-" prefix — SDK will re-add it
      meta[lower.slice("x-amz-meta-".length)] = value;
    }
  });
  return meta;
}
