/**
 * Multipart upload handlers.
 *
 * OPERATION MAP:
 *   CreateMultipartUpload   → proxied  (returns UploadId in XML)
 *   UploadPart              → 307 presigned redirect (data-plane)
 *   CompleteMultipartUpload → proxied  (assembles parts on backend)
 *   AbortMultipartUpload    → proxied  (signals backend + cleans KV)
 *   ListParts               → proxied  (returns part list from backend)
 *
 * STATE MANAGEMENT (Cloudflare KV):
 *   "upload:meta:{proxyId}" → UploadMeta (7-day TTL)
 *
 *   The proxy assigns a UUID as the client-facing UploadId. The real
 *   backend UploadId is stored in KV alongside the metadata needed to
 *   verify ownership on subsequent calls.
 *
 * OWNERSHIP VERIFICATION:
 *   Every UploadPart / Complete / Abort / ListParts call verifies that:
 *     - The proxyId is known (KV lookup)
 *     - The tenantId matches the current request's tenant
 *     - The bucket and key in the URL match the stored values
 *   All three must match, or we return NoSuchUpload (403 cross-tenant,
 *   404 genuine not-found — both surfaces return 404 to avoid oracle).
 *
 * PART ETAG TRACKING:
 *   Parts are uploaded directly to the backend via presigned URLs.
 *   The backend returns an ETag per part in the HTTP response to the
 *   client. The client then sends all ETags in CompleteMultipartUpload.
 *   We do NOT need to store ETags in KV — the client tracks them per
 *   the S3 protocol specification.
 *
 * CONCURRENCY:
 *   Multiple UploadPart requests may run concurrently (the S3 protocol
 *   requires this support). Each part gets its own presigned URL; there
 *   is no shared mutable state during the upload phase.
 */

import type { Context }      from "hono";
import type { CompletedPart } from "@aws-sdk/client-s3";
import {
  resolveBucket, resolveKey, makeClient,
  redirectResponse, xmlResponse, emptyResponse, presignExpiry,
} from "./common";
import { presignUploadPart }  from "@/backend/presigned";
import {
  parseCompleteMpu,
  xmlCreateMpu,
  xmlCompleteResult,
  xmlListParts,
} from "@/protocol/xml";
import { ProxyError }         from "@/errors";
import { validateObjectKey }  from "@/tenant";

// ─── KV state ─────────────────────────────────────────────────────────────────

interface UploadMeta {
  proxyUploadId:   string;
  backendUploadId: string;
  realKey:         string;
  virtualBucket:   string;
  virtualKey:      string;
  tenantId:        string;
  createdAt:       string;
}

const UPLOAD_TTL_S = 604_800; // 7 days — matches S3 multipart upload lifetime

// ─── CreateMultipartUpload ────────────────────────────────────────────────────

export async function handleCreateMultipartUpload(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);

  validateObjectKey(key);

  // Verify bucket exists
  const registry = c.get("registry");
  if (!await registry.bucketExists(ctx.record.tenantId, bucket)) {
    throw ProxyError.noSuchBucket(bucket);
  }

  const contentType = c.req.header("content-type") ?? "application/octet-stream";
  const client      = await makeClient(ctx);
  const realKey     = ctx.realKey(bucket, key);

  // Initiate on the backend (SDK handles the XML request/response)
  const backendId = await client.createMultipartUpload(realKey, contentType);
  const proxyId   = generateUploadId();

  const meta: UploadMeta = {
    proxyUploadId:   proxyId,
    backendUploadId: backendId,
    realKey,
    virtualBucket:   bucket,
    virtualKey:      key,
    tenantId:        ctx.record.tenantId,
    createdAt:       new Date().toISOString(),
  };

  const kv = (c.env as Env).UPLOAD_KV;
  await kv.put(`upload:meta:${proxyId}`, JSON.stringify(meta), {
    expirationTtl: UPLOAD_TTL_S,
  });

  return xmlResponse(xmlCreateMpu(bucket, key, proxyId), 200, reqId);
}

// ─── UploadPart → 307 presigned redirect ──────────────────────────────────────

export async function handleUploadPart(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);
  const kv     = (c.env as Env).UPLOAD_KV;

  const uploadId  = c.req.query("uploadId") ?? "";
  const partNumRaw = c.req.query("partNumber") ?? "";
  const partNum   = parseInt(partNumRaw, 10);

  if (!uploadId)                                    throw ProxyError.malformedXML("uploadId query param missing");
  if (!Number.isInteger(partNum) || partNum < 1 || partNum > 10_000) {
    throw ProxyError.invalidArgument(
      `partNumber must be an integer 1-10000, got: ${partNumRaw}`,
    );
  }

  const meta = await loadMeta(kv, uploadId);
  verifyOwnership(meta, ctx.record.tenantId, bucket, key, uploadId);

  const client  = await makeClient(ctx);
  const expires = Math.min(presignExpiry(c) * 4, 3600); // parts may be large; give more time

  const url = await presignUploadPart(
    client.rawClient,
    client.bucket,
    meta.realKey,
    meta.backendUploadId,
    partNum,
    { expiresIn: expires },
  );

  return redirectResponse(url, reqId);
}

// ─── CompleteMultipartUpload ──────────────────────────────────────────────────

export async function handleCompleteMultipartUpload(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);
  const kv     = (c.env as Env).UPLOAD_KV;

  const uploadId = c.req.query("uploadId") ?? "";
  if (!uploadId) throw ProxyError.malformedXML("uploadId query param missing");

  const meta = await loadMeta(kv, uploadId);
  verifyOwnership(meta, ctx.record.tenantId, bucket, key, uploadId);

  // Parse the client's <CompleteMultipartUpload> body
  const body = await c.req.text();
  if (!body.trim()) throw ProxyError.malformedXML("Request body is empty");

  let parsed;
  try {
    parsed = parseCompleteMpu(body);
  } catch (e) {
    throw ProxyError.malformedXML(e instanceof Error ? e.message : String(e));
  }

  if (parsed.parts.length === 0) {
    throw ProxyError.malformedXML("CompleteMultipartUpload must contain at least one Part");
  }

  // Validate strictly ascending part numbers (S3 spec §CompleteMultipartUpload)
  for (let i = 1; i < parsed.parts.length; i++) {
    const prev = parsed.parts[i - 1]!;
    const curr = parsed.parts[i]!;
    if (prev.PartNumber >= curr.PartNumber) {
      throw ProxyError.invalidPartOrder(
        `Part ${curr.PartNumber} must have a higher part number than part ${prev.PartNumber}`,
      );
    }
  }

  // Build the SDK CompletedPart array — ETags from the client are forwarded as-is
  const completedParts: CompletedPart[] = parsed.parts.map((p) => ({
    PartNumber: p.PartNumber,
    ETag:       p.ETag,
  }));

  const client = await makeClient(ctx);
  const result = await client.completeMultipartUpload(
    meta.realKey,
    meta.backendUploadId,
    completedParts,
  );

  // Async cleanup — fire-and-forget; TTL handles it if this fails
  c.executionCtx?.waitUntil(
    kv.delete(`upload:meta:${uploadId}`).catch(
      (err: unknown) => console.warn("Failed to delete upload meta:", err),
    ),
  );

  return xmlResponse(
    xmlCompleteResult(bucket, key, result.etag, result.location),
    200, reqId,
  );
}

// ─── AbortMultipartUpload ────────────────────────────────────────────────────

export async function handleAbortMultipartUpload(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);
  const kv     = (c.env as Env).UPLOAD_KV;

  const uploadId = c.req.query("uploadId") ?? "";
  if (!uploadId) throw ProxyError.malformedXML("uploadId query param missing");

  const meta = await loadMeta(kv, uploadId);
  verifyOwnership(meta, ctx.record.tenantId, bucket, key, uploadId);

  const client = await makeClient(ctx);
  await client.abortMultipartUpload(meta.realKey, meta.backendUploadId);

  c.executionCtx?.waitUntil(
    kv.delete(`upload:meta:${uploadId}`).catch(
      (err: unknown) => console.warn("Failed to delete upload meta:", err),
    ),
  );

  return emptyResponse(204, reqId);
}

// ─── ListParts ───────────────────────────────────────────────────────────────

export async function handleListParts(c: Context): Promise<Response> {
  const ctx    = c.get("tenantCtx");
  const reqId  = c.get("requestId");
  const bucket = resolveBucket(c);
  const key    = resolveKey(c);
  const kv     = (c.env as Env).UPLOAD_KV;

  const uploadId         = c.req.query("uploadId") ?? "";
  const maxPartsRaw      = c.req.query("max-parts")           ?? "1000";
  const partMarkerRaw    = c.req.query("part-number-marker")  ?? "";

  if (!uploadId) throw ProxyError.malformedXML("uploadId query param missing");

  const maxParts  = Math.min(parseInt(maxPartsRaw, 10) || 1000, 1000);
  const partMarker = partMarkerRaw ? parseInt(partMarkerRaw, 10) : undefined;

  const meta = await loadMeta(kv, uploadId);
  verifyOwnership(meta, ctx.record.tenantId, bucket, key, uploadId);

  const client = await makeClient(ctx);
  const result = await client.listParts(meta.realKey, meta.backendUploadId, maxParts, partMarker);

  const parts = (result.parts ?? []).map((p) => ({
    partNumber:   p.PartNumber   ?? 0,
    etag:         p.ETag         ?? "",
    size:         BigInt(p.Size  ?? 0),
    lastModified: p.LastModified?.toISOString() ?? "",
  }));

  return xmlResponse(
    xmlListParts(
      bucket, key, uploadId,
      result.isTruncated,
      result.nextPartNumber,
      parts,
    ),
    200, reqId,
  );
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

async function loadMeta(kv: KVNamespace, proxyId: string): Promise<UploadMeta> {
  // Sanitise the proxyId before using it as a KV key
  if (!/^[0-9a-f-]{36}$/i.test(proxyId)) {
    throw ProxyError.noSuchUpload(proxyId);
  }
  const raw = await kv.get(`upload:meta:${proxyId}`);
  if (!raw) throw ProxyError.noSuchUpload(proxyId);
  try {
    return JSON.parse(raw) as UploadMeta;
  } catch {
    throw ProxyError.internal("Upload metadata is corrupt");
  }
}

function verifyOwnership(
  meta:     UploadMeta,
  tenantId: string,
  bucket:   string,
  key:      string,
  proxyId:  string,
): void {
  // All three conditions must match — partial matches are treated identically
  // to avoid oracle attacks (an attacker can't distinguish "wrong tenant" from
  // "wrong bucket" from "upload not found")
  if (
    meta.tenantId      !== tenantId ||
    meta.virtualBucket !== bucket   ||
    meta.virtualKey    !== key
  ) {
    throw ProxyError.noSuchUpload(proxyId);
  }
}

function generateUploadId(): string {
  // Standard UUID v4 format — compatible with all S3 client parsers
  return crypto.randomUUID();
}
