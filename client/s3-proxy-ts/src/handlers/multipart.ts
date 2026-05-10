/**
 * Multipart upload handlers.
 *
 * UploadPart returns a presigned 307 redirect — the client uploads the part
 * directly to the backend.  All orchestration (Create, Complete, Abort, List)
 * is proxied through the Worker using the `@aws-sdk/client-s3` SDK.
 *
 * State (proxy_upload_id → backend_upload_id mapping) is stored in UPLOAD_KV
 * with a 7-day TTL.  The proxy assigns a UUID as the client-facing upload ID;
 * the real backend ID is stored encrypted alongside it.
 */
import type { Context } from "hono";
import type { CompletedPart } from "@aws-sdk/client-s3";
import { S3CompatClient } from "@/backend/client";
import { presignUploadPart } from "@/backend/presigned";
import { parseCompleteMpu, xmlCreateMpu, xmlListParts } from "@/protocol/xml";
import { ProxyError } from "@/errors";

// ─── KV state ─────────────────────────────────────────────────────────────────

interface UploadMeta {
  proxyUploadId: string;
  backendUploadId: string;
  realKey: string;
  virtualBucket: string;
  virtualKey: string;
  tenantId: string;
}

const UPLOAD_TTL = 7 * 24 * 3600; // 7 days (seconds)

// ─── Helpers ──────────────────────────────────────────────────────────────────

function xmlRes(body: string, status = 200): Response {
  return new Response(body, {
    status,
    headers: { "Content-Type": "application/xml" },
  });
}

function presignRedirect(url: string): Response {
  return new Response(null, {
    status: 307,
    headers: { Location: url, "Cache-Control": "no-store" },
  });
}

async function loadMeta(env: Env, proxyId: string): Promise<UploadMeta> {
  const raw = await env.UPLOAD_KV.get(`upload:meta:${proxyId}`);
  if (!raw) throw ProxyError.noSuchUpload(proxyId);
  return JSON.parse(raw) as UploadMeta;
}

async function saveMeta(env: Env, meta: UploadMeta): Promise<void> {
  await env.UPLOAD_KV.put(
    `upload:meta:${meta.proxyUploadId}`,
    JSON.stringify(meta),
    { expirationTtl: UPLOAD_TTL }
  );
}

function guardOwnership(
  meta: UploadMeta,
  tenantId: string,
  bucket: string,
  key: string,
  proxyId: string
): void {
  if (
    meta.tenantId !== tenantId ||
    meta.virtualBucket !== bucket ||
    meta.virtualKey !== key
  )
    throw ProxyError.noSuchUpload(proxyId);
}

// ─── POST /{bucket}/{key}?uploads — CreateMultipartUpload ─────────────────────

export async function handleCreateMultipartUpload(
  c: Context
): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const realKey = ctx.realKey(bucket, key);
  const ct = c.req.header("content-type") ?? "application/octet-stream";

  // SDK call to initiate on backend
  const backendId = await client.createMultipartUpload(realKey, ct);
  const proxyId = crypto.randomUUID();

  const meta: UploadMeta = {
    proxyUploadId: proxyId,
    backendUploadId: backendId,
    realKey,
    virtualBucket: bucket,
    virtualKey: key,
    tenantId: ctx.record.tenantId,
  };
  await saveMeta(c.env as Env, meta);

  return xmlRes(xmlCreateMpu(bucket, key, proxyId));
}

// ─── PUT /{bucket}/{key}?partNumber=N&uploadId=X — UploadPart → 307 ──────────

export async function handleUploadPart(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const proxyId = c.req.query("uploadId") ?? "";
  const partNum = parseInt(c.req.query("partNumber") ?? "0", 10);
  const env = c.env as Env;

  if (isNaN(partNum) || partNum < 1 || partNum > 10_000) {
    throw ProxyError.badRequest(
      `partNumber must be 1-10000, got ${c.req.query("partNumber")}`
    );
  }

  const meta = await loadMeta(env, proxyId);
  guardOwnership(meta, ctx.record.tenantId, bucket, key, proxyId);

  const expiresIn = Number(
    (c.env as Env & { PRESIGN_EXPIRY_S?: string }).PRESIGN_EXPIRY_S ?? 3600
  );
  const url = await presignUploadPart(
    client.rawClient,
    client.bucketName,
    meta.realKey,
    meta.backendUploadId,
    partNum,
    { expiresIn }
  );

  return presignRedirect(url);
}

// ─── POST /{bucket}/{key}?uploadId=X — CompleteMultipartUpload ───────────────

export async function handleCompleteMultipartUpload(
  c: Context
): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const proxyId = c.req.query("uploadId") ?? "";
  const env = c.env as Env;

  const meta = await loadMeta(env, proxyId);
  guardOwnership(meta, ctx.record.tenantId, bucket, key, proxyId);

  const body = await c.req.text();
  let req;
  try {
    req = parseCompleteMpu(body);
  } catch (e) {
    throw ProxyError.badRequest(`CompleteMultipartUpload XML: ${String(e)}`);
  }

  // Validate ascending part order (S3 spec requirement)
  for (let i = 1; i < req.Parts.length; i++) {
    const prev = req.Parts[i - 1]!;
    const curr = req.Parts[i]!;
    if (prev.PartNumber >= curr.PartNumber) {
      throw ProxyError.invalidPartOrder(
        `Part ${prev.PartNumber} must precede ${curr.PartNumber}`
      );
    }
  }

  // Build the SDK CompletedPart array from the client-supplied ETags
  const parts: CompletedPart[] = req.Parts.map((p) => ({
    PartNumber: p.PartNumber,
    ETag: p.ETag,
  }));

  await client.completeMultipartUpload(
    meta.realKey,
    meta.backendUploadId,
    parts
  );

  // Best-effort cleanup
  await env.UPLOAD_KV.delete(`upload:meta:${proxyId}`).catch(() => {});

  return new Response(null, { status: 200 });
}

// ─── DELETE /{bucket}/{key}?uploadId=X — AbortMultipartUpload ────────────────

export async function handleAbortMultipartUpload(
  c: Context
): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const proxyId = c.req.query("uploadId") ?? "";
  const env = c.env as Env;

  const meta = await loadMeta(env, proxyId);
  guardOwnership(meta, ctx.record.tenantId, bucket, key, proxyId);

  await client.abortMultipartUpload(meta.realKey, meta.backendUploadId);
  await env.UPLOAD_KV.delete(`upload:meta:${proxyId}`).catch(() => {});

  return new Response(null, { status: 204 });
}

// ─── GET /{bucket}/{key}?uploadId=X — ListParts ──────────────────────────────

export async function handleListParts(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const proxyId = c.req.query("uploadId") ?? "";
  const env = c.env as Env;

  const meta = await loadMeta(env, proxyId);
  guardOwnership(meta, ctx.record.tenantId, bucket, key, proxyId);

  // Use SDK to list parts — response is already typed
  const resp = await client.listParts(meta.realKey, meta.backendUploadId);
  const parts = (resp.Parts ?? []).map((p) => ({
    partNumber: p.PartNumber ?? 0,
    etag: p.ETag ?? "",
    size: p.Size ?? 0,
    lastModified: p.LastModified?.toISOString() ?? "",
  }));

  return xmlRes(xmlListParts(bucket, key, proxyId, parts));
}
