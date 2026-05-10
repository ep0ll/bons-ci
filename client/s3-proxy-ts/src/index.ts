/**
 * S3-compatible multi-tenant proxy — Cloudflare Workers entry point.
 *
 * Uses `hono` as the web framework for routing and middleware composition.
 * The route table maps every S3 API surface (virtual-hosted and path-style)
 * to a typed handler.  A single auth middleware guards all routes.
 *
 * # Route priority (Hono resolves top-to-bottom)
 * Specific query-parameter routes (e.g. ?uploads, ?delete) are matched
 * before their generic counterparts (GET /{bucket}).
 *
 * @see https://hono.dev/docs/getting-started/cloudflare-workers
 */
import { Hono } from "hono";
import { logger } from "hono/logger";
import { cors } from "hono/cors";

import { s3AuthMiddleware } from "@/auth/middleware";
import { withErrorBoundary, ProxyError } from "@/errors";

import {
  handleListBuckets,
  handleHeadBucket,
  handleCreateBucket,
  handleDeleteBucket,
} from "@/handlers/buckets";

import {
  handleGetObject,
  handlePutObject,
  handleHeadObject,
  handleDeleteObject,
  handleDeleteObjects,
  handleCopyObject,
  handleListObjects,
} from "@/handlers/objects";

import {
  handleCreateMultipartUpload,
  handleUploadPart,
  handleCompleteMultipartUpload,
  handleAbortMultipartUpload,
  handleListParts,
} from "@/handlers/multipart";

// ─── Env type (bound to wrangler.toml) ───────────────────────────────────────

declare global {
  interface Env {
    TENANT_KV: KVNamespace;
    UPLOAD_KV: KVNamespace;
    MASTER_ENC_KEY: string; // Workers Secret
    PROXY_DOMAIN: string;
    LOG_LEVEL: string;
    PRESIGN_EXPIRY_S: string;
  }
}

// ─── App ─────────────────────────────────────────────────────────────────────

const app = new Hono<{ Bindings: Env }>();

// ── Global middleware ─────────────────────────────────────────────────────────

// Structured request logging (honours LOG_LEVEL env var)
app.use("*", logger());

// CORS — S3 SDKs don't issue preflight, but browser-based access may need it
app.use(
  "*",
  cors({
    origin: "*",
    allowMethods: ["GET", "PUT", "POST", "DELETE", "HEAD"],
    allowHeaders: [
      "Authorization",
      "Content-Type",
      "x-amz-date",
      "x-amz-content-sha256",
      "x-amz-security-token",
      "x-amz-copy-source",
      "x-amz-metadata-directive",
    ],
    exposeHeaders: ["ETag", "x-amz-request-id"],
  })
);

// SigV4 authentication — injects TenantContext into all downstream handlers
app.use("*", (c, next) => s3AuthMiddleware(c.env)(c, next));

// ─── Route table ──────────────────────────────────────────────────────────────
// Both virtual-hosted (Host: {bucket}.s3.example.com) and path-style
// (s3.example.com/{bucket}/{key}) are handled by the same handlers.
// Hono params capture the bucket from the first path segment in path-style;
// a middleware normalises virtual-hosted requests to path-style internally.

// ── Account level ─────────────────────────────────────────────────────────────
app.get("/", (c) => withErrorBoundary(() => handleListBuckets(c)));

// ── Bucket level ──────────────────────────────────────────────────────────────
app.head("/:bucket", (c) => withErrorBoundary(() => handleHeadBucket(c)));
app.put("/:bucket", (c) => withErrorBoundary(() => handleCreateBucket(c)));
app.delete("/:bucket", (c) => withErrorBoundary(() => handleDeleteBucket(c)));

// ListObjects (must be before object routes)
app.get("/:bucket", (c) => withErrorBoundary(() => handleListObjects(c)));

// DeleteObjects batch
app.post("/:bucket", async (c) => {
  if (c.req.query("delete") !== undefined) {
    return withErrorBoundary(() => handleDeleteObjects(c));
  }
  return ProxyError.notImplemented("POST /{bucket}").toResponse();
});

// ── Object level ──────────────────────────────────────────────────────────────

// Multipart: Create (POST ?uploads)
app.post("/:bucket/:key{.+}", (c) =>
  withErrorBoundary(async () => {
    const qp = c.req.query;
    if (qp("uploads") !== undefined) return handleCreateMultipartUpload(c);
    if (qp("uploadId")) return handleCompleteMultipartUpload(c);
    return ProxyError.notImplemented("POST /{bucket}/{key}").toResponse();
  })
);

// GET — ListParts (?uploadId) or GetObject
app.get("/:bucket/:key{.+}", (c) =>
  withErrorBoundary(async () => {
    if (c.req.query("uploadId")) return handleListParts(c);
    return handleGetObject(c);
  })
);

// PUT — UploadPart (?uploadId&partNumber) or CopyObject (x-amz-copy-source) or PutObject
app.put("/:bucket/:key{.+}", (c) =>
  withErrorBoundary(async () => {
    if (c.req.query("uploadId")) return handleUploadPart(c);
    if (c.req.header("x-amz-copy-source")) return handleCopyObject(c);
    return handlePutObject(c);
  })
);

// DELETE — AbortMultipartUpload (?uploadId) or DeleteObject
app.delete("/:bucket/:key{.+}", (c) =>
  withErrorBoundary(async () => {
    if (c.req.query("uploadId")) return handleAbortMultipartUpload(c);
    return handleDeleteObject(c);
  })
);

app.head("/:bucket/:key{.+}", (c) =>
  withErrorBoundary(() => handleHeadObject(c))
);

// ── Fallback ──────────────────────────────────────────────────────────────────
app.all("*", (c) =>
  ProxyError.notImplemented(`${c.req.method} ${c.req.path}`).toResponse()
);

// ─── Export for Cloudflare Workers ────────────────────────────────────────────
export default app;
