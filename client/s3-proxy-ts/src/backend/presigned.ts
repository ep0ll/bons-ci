/**
 * Presigned URL generation using `@aws-sdk/s3-request-presigner`.
 *
 * One `getSignedUrl()` call handles all SigV4 query-string signing:
 * canonical request construction, key derivation, URL encoding, and
 * parameter ordering — all conformant with AWS spec out of the box.
 *
 * All data-plane operations (GET, PUT, UploadPart) return a `307 Temporary
 * Redirect` to the presigned URL. The client talks directly to B2/S3/MinIO.
 *
 * WHY 307 NOT 302:
 *   RFC 9110 §15.4.8 — 307 preserves the HTTP method on redirect.
 *   A 302 on PUT silently becomes a GET (losing the body). AWS SDKs
 *   correctly follow 307 on PUT/DELETE. This is non-negotiable.
 *
 * WHY NOT 303:
 *   303 is "See Other" and always GET. Never appropriate for PUT.
 */

import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import {
  GetObjectCommand,
  PutObjectCommand,
  UploadPartCommand,
  HeadObjectCommand,
  DeleteObjectCommand,
} from "@aws-sdk/client-s3";
import type { S3Client } from "@aws-sdk/client-s3";
import { ProxyError } from "@/errors";

// ─── Options ──────────────────────────────────────────────────────────────────

export interface PresignOptions {
  expiresIn?:  number;  // seconds; default 900, max 604800
  conditions?: { contentType?: string };
}

// ─── URL generators ───────────────────────────────────────────────────────────

export async function presignGet(
  client:  S3Client,
  bucket:  string,
  key:     string,
  opts?:   PresignOptions,
): Promise<string> {
  return safePresign(() =>
    getSignedUrl(client, new GetObjectCommand({ Bucket: bucket, Key: key }), {
      expiresIn: opts?.expiresIn ?? 900,
    }),
  );
}

export async function presignPut(
  client:  S3Client,
  bucket:  string,
  key:     string,
  opts?:   PresignOptions & { contentType?: string; metadata?: Record<string, string> },
): Promise<string> {
  return safePresign(() =>
    getSignedUrl(
      client,
      new PutObjectCommand({
        Bucket:      bucket,
        Key:         key,
        ContentType: opts?.contentType,
        Metadata:    opts?.metadata,
      }),
      { expiresIn: opts?.expiresIn ?? 900 },
    ),
  );
}

export async function presignHead(
  client:  S3Client,
  bucket:  string,
  key:     string,
  opts?:   PresignOptions,
): Promise<string> {
  return safePresign(() =>
    getSignedUrl(client, new HeadObjectCommand({ Bucket: bucket, Key: key }), {
      expiresIn: opts?.expiresIn ?? 900,
    }),
  );
}

export async function presignDelete(
  client:  S3Client,
  bucket:  string,
  key:     string,
  opts?:   PresignOptions,
): Promise<string> {
  return safePresign(() =>
    getSignedUrl(client, new DeleteObjectCommand({ Bucket: bucket, Key: key }), {
      expiresIn: opts?.expiresIn ?? 900,
    }),
  );
}

export async function presignUploadPart(
  client:     S3Client,
  bucket:     string,
  key:        string,
  uploadId:   string,
  partNumber: number,
  opts?:      PresignOptions,
): Promise<string> {
  if (partNumber < 1 || partNumber > 10_000) {
    throw ProxyError.invalidArgument(`partNumber must be 1-10000, got ${partNumber}`);
  }
  return safePresign(() =>
    getSignedUrl(
      client,
      new UploadPartCommand({
        Bucket:     bucket,
        Key:        key,
        UploadId:   uploadId,
        PartNumber: partNumber,
      }),
      { expiresIn: opts?.expiresIn ?? 3600 },
    ),
  );
}

// ─── Redirect response builder ────────────────────────────────────────────────

/**
 * Build a `307 Temporary Redirect` response to a presigned URL.
 *
 * Headers set:
 * - `Location`      — the presigned URL
 * - `Cache-Control` — `no-store` (presigned URLs are ephemeral)
 * - `x-amz-request-id` — for client-side log correlation
 */
export function presignedRedirect(url: string, requestId: string): Response {
  return new Response(null, {
    status: 307,
    headers: {
      "Location":          url,
      "Cache-Control":     "no-store",
      "x-amz-request-id": requestId,
    },
  });
}

// ─── Safe wrapper ─────────────────────────────────────────────────────────────

async function safePresign(fn: () => Promise<string>): Promise<string> {
  try {
    return await fn();
  } catch (err) {
    throw ProxyError.from(err, "presign");
  }
}
