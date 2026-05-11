/**
 * S3-compatible multi-tenant proxy — Cloudflare Workers entry point.
 *
 * Uses Hono as the web framework. Routes are ordered from most-specific
 * to least-specific so query-parameter-differentiated operations (e.g.
 * ?uploads vs ?uploadId vs plain POST) are matched correctly.
 *
 * VIRTUAL-HOSTED STYLE:
 *   The virtualHostMiddleware normalises {bucket}.s3.example.com/{key}
 *   requests by injecting the bucket into the Hono context. All handlers
 *   call `resolveBucket(c)` which checks both path params and context.
 *
 * ERROR HANDLING:
 *   Every route is wrapped in `withErrorBoundary` which catches any
 *   thrown `ProxyError` (or unknown errors) and returns an S3-compatible
 *   XML error response. No error leaks through as an unhandled rejection.
 *
 * REQUEST ID:
 *   Every request gets a unique `requestId` (set by auth middleware) that
 *   appears in `x-amz-request-id` headers for client-side log correlation.
 */

import { Hono }   from "hono";
import { logger } from "hono/logger";

import { parseEnv }                   from "@/env";
import { s3AuthMiddleware }            from "@/middleware/auth";
import { virtualHostMiddleware }       from "@/middleware/virtual-host";
import { withErrorBoundary, ProxyError } from "@/errors";

import { handleListBuckets, handleHeadBucket, handleCreateBucket, handleDeleteBucket }
  from "@/handlers/buckets";
import {
  handleGetObject, handlePutObject, handleHeadObject,
  handleDeleteObject, handleDeleteObjects, handleCopyObject, handleListObjects,
} from "@/handlers/objects";
import {
  handleCreateMultipartUpload, handleUploadPart,
  handleCompleteMultipartUpload, handleAbortMultipartUpload, handleListParts,
} from "@/handlers/multipart";

// ─── App ─────────────────────────────────────────────────────────────────────

const app = new Hono<{ Bindings: Env }>();

// ─── Global middleware ────────────────────────────────────────────────────────

// Structured logging — respects LOG_LEVEL env var
app.use("*", logger());

// Virtual-hosted bucket normalisation (must run before auth)
app.use("*", (c, next) => {
  try {
    const config = parseEnv(c.env);
    return virtualHostMiddleware(config)(c, next);
  } catch (err) {
    return (err instanceof ProxyError ? err : ProxyError.from(err, "init")).toResponse();
  }
});

// SigV4 authentication — injects TenantContext into all downstream handlers
app.use("*", (c, next) => {
  let config;
  try {
    config = parseEnv(c.env);
  } catch (err) {
    return Promise.resolve(
      (err instanceof ProxyError ? err : ProxyError.from(err, "env")).toResponse(),
    );
  }
  return s3AuthMiddleware(c.env, config)(c, next);
});

// ─── Route table ─────────────────────────────────────────────────────────────
// Hono matches routes top-to-bottom; more specific routes must appear first.

// ── Account level ─────────────────────────────────────────────────────────────
app.get("/", (c) => withErrorBoundary(() => handleListBuckets(c)));

// ── Bucket level ──────────────────────────────────────────────────────────────
app.head("/:bucket",   (c) => withErrorBoundary(() => handleHeadBucket(c)));
app.put( "/:bucket",   (c) => withErrorBoundary(() => handleCreateBucket(c)));
app.delete("/:bucket", (c) => withErrorBoundary(() => handleDeleteBucket(c)));

// DeleteObjects — POST /{bucket}?delete
app.post("/:bucket", (c) => withErrorBoundary(async () => {
  if (c.req.query("delete") !== undefined) return handleDeleteObjects(c);
  throw ProxyError.notImplemented(`POST /${c.req.param("bucket")}`);
}));

// ListObjects — GET /{bucket} (must be after bucket-level HEAD/PUT/DELETE)
app.get("/:bucket", (c) => withErrorBoundary(() => handleListObjects(c)));

// ── Object level ──────────────────────────────────────────────────────────────

// POST /{bucket}/{key} — CreateMultipartUpload or CompleteMultipartUpload
app.post("/:bucket/:key{.+}", (c) => withErrorBoundary(async () => {
  if (c.req.query("uploads") !== undefined) return handleCreateMultipartUpload(c);
  if (c.req.query("uploadId"))              return handleCompleteMultipartUpload(c);
  throw ProxyError.notImplemented(`POST /${c.req.param("bucket")}/${c.req.param("key")}`);
}));

// GET /{bucket}/{key} — GetObject or ListParts
app.get("/:bucket/:key{.+}", (c) => withErrorBoundary(async () => {
  if (c.req.query("uploadId")) return handleListParts(c);
  return handleGetObject(c);
}));

// PUT /{bucket}/{key} — UploadPart, CopyObject, or PutObject
app.put("/:bucket/:key{.+}", (c) => withErrorBoundary(async () => {
  if (c.req.query("uploadId"))                    return handleUploadPart(c);
  if (c.req.header("x-amz-copy-source"))          return handleCopyObject(c);
  return handlePutObject(c);
}));

// DELETE /{bucket}/{key} — AbortMultipartUpload or DeleteObject
app.delete("/:bucket/:key{.+}", (c) => withErrorBoundary(async () => {
  if (c.req.query("uploadId")) return handleAbortMultipartUpload(c);
  return handleDeleteObject(c);
}));

app.head("/:bucket/:key{.+}", (c) => withErrorBoundary(() => handleHeadObject(c)));

// ── Catch-all ─────────────────────────────────────────────────────────────────
app.all("*", (c) =>
  ProxyError.notImplemented(`${c.req.method} ${c.req.path}`).toResponse(),
);

// ─── Cloudflare Workers export ────────────────────────────────────────────────
export default app;
