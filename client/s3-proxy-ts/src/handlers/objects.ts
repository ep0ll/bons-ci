/**
 * Object-level handlers.
 *
 * Data-plane (GET, PUT) → 307 presigned redirect via `@aws-sdk/s3-request-presigner`.
 * Control-plane (HEAD, DELETE, List, Copy) → direct SDK calls.
 */
import type { Context } from "hono";
import { S3CompatClient } from "@/backend/client";
import { presignGetObject, presignPutObject } from "@/backend/presigned";
import {
  parseDeleteRequest,
  xmlDeleteResult,
  xmlListObjectsV2,
  xmlListObjectsV1,
  type ListEntry,
} from "@/protocol/xml";
import { ProxyError } from "@/errors";

// ─── Shared helpers ────────────────────────────────────────────────────────────

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

async function makeClient(
  c: Context
): Promise<{ client: S3CompatClient; ctx: ReturnType<Context["get"]> }> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  return { client, ctx };
}

// ─── GET /{bucket}/{key} — presigned redirect ─────────────────────────────────

export async function handleGetObject(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const realKey = ctx.realKey(bucket, key);

  const expiresIn = Number(c.env?.PRESIGN_EXPIRY_S ?? 900);
  const url = await presignGetObject(
    client.rawClient,
    client.bucketName,
    realKey,
    { expiresIn }
  );

  return presignRedirect(url);
}

// ─── PUT /{bucket}/{key} — presigned redirect ─────────────────────────────────

export async function handlePutObject(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const realKey = ctx.realKey(bucket, key);

  const contentType =
    c.req.header("content-type") ?? "application/octet-stream";
  const expiresIn = Number(c.env?.PRESIGN_EXPIRY_S ?? 900);
  const url = await presignPutObject(
    client.rawClient,
    client.bucketName,
    realKey,
    {
      contentType,
      expiresIn,
    }
  );

  return presignRedirect(url);
}

// ─── HEAD /{bucket}/{key} — proxied ──────────────────────────────────────────

export async function handleHeadObject(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const realKey = ctx.realKey(bucket, key);

  const meta = await client.headObject(realKey);
  return new Response(null, { status: 200, headers: meta });
}

// ─── DELETE /{bucket}/{key} — proxied ────────────────────────────────────────

export async function handleDeleteObject(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");
  const key = c.req.param("key") ?? "";
  const realKey = ctx.realKey(bucket, key);

  await client.deleteObject(realKey);
  return new Response(null, { status: 204 });
}

// ─── POST /{bucket}?delete — DeleteObjects (batch) ───────────────────────────

export async function handleDeleteObjects(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");

  const body = await c.req.text();
  let req;
  try {
    req = parseDeleteRequest(body);
  } catch (e) {
    throw ProxyError.badRequest(`DeleteObjects XML parse error: ${String(e)}`);
  }

  if (req.Objects.length > 1000) {
    throw ProxyError.badRequest("DeleteObjects: max 1000 keys per request");
  }

  // Map virtual keys to real backend keys
  const realKeyMap = new Map(
    req.Objects.map((o) => [ctx.realKey(bucket, o.Key), o.Key])
  );
  const realKeys = [...realKeyMap.keys()];

  // Use SDK batch delete (handles XML + chunking internally)
  const result = await client.deleteObjects(realKeys);

  // Map real keys back to virtual keys for the response
  const deleted = result.deleted.map((rk) => realKeyMap.get(rk) ?? rk);
  const errors = result.errors.map(({ key: rk, message }) => ({
    key: realKeyMap.get(rk) ?? rk,
    message,
  }));

  return xmlRes(xmlDeleteResult(deleted, errors));
}

// ─── PUT /{bucket}/{key} with x-amz-copy-source — CopyObject ─────────────────

export async function handleCopyObject(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const dstBucket = c.req.param("bucket");
  const dstKey = c.req.param("key") ?? "";

  const copySource = c.req.header("x-amz-copy-source") ?? "";
  const src = copySource.replace(/^\//, "");
  const slashIdx = src.indexOf("/");
  if (slashIdx < 0)
    throw ProxyError.badRequest(`Invalid x-amz-copy-source: ${copySource}`);

  const srcBucket = src.slice(0, slashIdx);
  const srcKey = src.slice(slashIdx + 1);

  const srcReal = ctx.realKey(srcBucket, srcKey);
  const dstReal = ctx.realKey(dstBucket, dstKey);

  await client.copyObject(srcReal, dstReal);
  return new Response(null, { status: 200 });
}

// ─── GET /{bucket} — ListObjects ─────────────────────────────────────────────

export async function handleListObjects(c: Context): Promise<Response> {
  const ctx = c.get("tenantCtx");
  const secretKey = await ctx.backendSecret();
  const client = new S3CompatClient(ctx.record.defaultBackend, secretKey);
  const bucket = c.req.param("bucket");

  const qp = c.req.query;
  const listType = qp("list-type");
  const prefix = qp("prefix") ?? "";
  const delimiter = qp("delimiter") ?? "";
  const maxKeys = Math.min(parseInt(qp("max-keys") ?? "1000", 10), 1000);
  const contToken = qp("continuation-token") ?? qp("marker") ?? undefined;
  const startAfter = qp("start-after") ?? undefined;

  // Prepend the tenant+bucket prefix to the client's prefix
  const bucketPrefix = ctx.bucketPrefix(bucket);
  const realPrefix = `${bucketPrefix}${prefix}`;

  const listing = await client.listObjectsV2({
    prefix: realPrefix,
    delimiter: delimiter || undefined,
    maxKeys,
    continuationToken: contToken,
  });

  // Strip tenant+bucket prefix from keys before returning to client
  const contents: ListEntry[] = (listing.Contents ?? []).map((obj) => ({
    key: (obj.Key ?? "").replace(bucketPrefix, ""),
    lastModified: obj.LastModified?.toISOString() ?? "",
    etag: obj.ETag ?? "",
    size: obj.Size ?? 0,
    storageClass: obj.StorageClass ?? "STANDARD",
  }));

  const commonPrefixes = (listing.CommonPrefixes ?? []).map((cp) =>
    (cp.Prefix ?? "").replace(bucketPrefix, "")
  );

  const nextToken = listing.NextContinuationToken;

  if (listType === "2") {
    return xmlRes(
      xmlListObjectsV2({
        bucket,
        prefix,
        delimiter,
        maxKeys,
        isTruncated: listing.IsTruncated ?? false,
        keyCount: listing.KeyCount ?? contents.length,
        nextContinuationToken: nextToken,
        continuationToken: contToken,
        startAfter,
        contents,
        commonPrefixes,
      })
    );
  }

  // ListObjects V1
  const nextMarker =
    listing.IsTruncated && contents.length > 0
      ? contents[contents.length - 1]?.key
      : undefined;

  return xmlRes(
    xmlListObjectsV1({
      bucket,
      prefix,
      delimiter,
      marker: contToken ?? "",
      maxKeys,
      isTruncated: listing.IsTruncated ?? false,
      nextMarker,
      contents,
      commonPrefixes,
    })
  );
}
