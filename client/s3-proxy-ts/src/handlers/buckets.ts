/**
 * Bucket-level handlers — all pure metadata (KV), no backend calls except
 * for the emptiness check before DeleteBucket.
 */
import type { Context } from "hono";
import { xmlListBuckets } from "@/protocol/xml";
import { S3CompatClient } from "@/backend/client";
import { ProxyError } from "@/errors";
import { decrypt } from "@/crypto";

function xmlRes(body: string, status = 200): Response {
  return new Response(body, {
    status,
    headers: { "Content-Type": "application/xml" },
  });
}

// ── GET / — ListBuckets ───────────────────────────────────────────────────────

export async function handleListBuckets(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const registry = c.get("registry");
  const buckets = await registry.listBuckets(ctx.record.tenantId);
  return xmlRes(xmlListBuckets(ctx.record.tenantId, buckets));
}

// ── HEAD /{bucket} ────────────────────────────────────────────────────────────

export async function handleHeadBucket(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const registry = c.get("registry");
  const bucket = c.req.param("bucket");

  const exists = await registry.bucketExists(ctx.record.tenantId, bucket);
  if (!exists) throw ProxyError.noSuchBucket(bucket);

  return new Response(null, { status: 200 });
}

// ── PUT /{bucket} — CreateBucket ──────────────────────────────────────────────

export async function handleCreateBucket(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const registry = c.get("registry");
  const bucket = c.req.param("bucket");

  await registry.createBucket(ctx.record.tenantId, bucket);

  return new Response(null, {
    status: 200,
    headers: { Location: `/${bucket}` },
  });
}

// ── DELETE /{bucket} — DeleteBucket ───────────────────────────────────────────

export async function handleDeleteBucket(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const registry = c.get("registry");
  const bucket = c.req.param("bucket");
  const env = c.env as Env;

  // Verify bucket belongs to this tenant
  const exists = await registry.bucketExists(ctx.record.tenantId, bucket);
  if (!exists) throw ProxyError.noSuchBucket(bucket);

  // Check emptiness by listing one object under the bucket prefix
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const listing = await client.listObjectsV2({
    prefix: ctx.bucketPrefix(bucket),
    maxKeys: 1,
  });

  if ((listing.KeyCount ?? 0) > 0) throw ProxyError.bucketNotEmpty(bucket);

  await registry.deleteBucket(ctx.record.tenantId, bucket);
  return new Response(null, { status: 204 });
}
