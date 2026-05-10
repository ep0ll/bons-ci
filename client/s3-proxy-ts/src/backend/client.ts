/**
 * S3-compatible backend client for control-plane operations.
 *
 * Wraps `@aws-sdk/client-s3` with a custom endpoint so the same client works
 * with **Backblaze B2**, **Amazon S3**, and **MinIO** — any S3-compatible backend.
 *
 * Data-plane operations (GET, PUT, UploadPart) are handled via presigned URL
 * redirects — see `src/backend/presigned.ts`.  This module handles only
 * control-plane ops: HEAD, DELETE, List, Copy, and multipart orchestration.
 *
 * @see https://docs.aws.amazon.com/AWSJavaScriptSDK/v3/latest/client/s3/
 */
import {
  S3Client,
  HeadObjectCommand,
  DeleteObjectCommand,
  CopyObjectCommand,
  ListObjectsV2Command,
  CreateMultipartUploadCommand,
  CompleteMultipartUploadCommand,
  AbortMultipartUploadCommand,
  ListPartsCommand,
  DeleteObjectsCommand,
  type ListObjectsV2CommandOutput,
  type CompletedPart,
  type ObjectIdentifier,
} from "@aws-sdk/client-s3";
import { ProxyError } from "@/errors";
import type { BackendConfig } from "@/tenant/types";

// ─── Client factory ───────────────────────────────────────────────────────────

/**
 * Create an `@aws-sdk/client-s3` instance configured for a specific tenant backend.
 *
 * The AWS SDK v3 uses `fetch` under the hood when running in environments
 * without Node.js `http` — Cloudflare Workers qualify automatically.
 */
export function makeS3Client(
  config: BackendConfig,
  secretKey: string
): S3Client {
  const endpoint = config.endpoint.startsWith("http")
    ? config.endpoint
    : `https://${config.endpoint}`;

  return new S3Client({
    region: config.region,
    endpoint,
    credentials: {
      accessKeyId: config.accessKeyId,
      secretAccessKey: secretKey,
    },
    forcePathStyle: config.forcePathStyle, // required for MinIO
    // CF Workers provide `fetch` globally — SDK picks it up automatically
  });
}

// ─── S3CompatClient ───────────────────────────────────────────────────────────

/** Thin facade over S3Client that translates SDK responses to our domain types. */
export class S3CompatClient {
  private readonly client: S3Client;
  private readonly bucket: string;

  constructor(config: BackendConfig, secretKey: string) {
    this.client = makeS3Client(config, secretKey);
    this.bucket = config.bucket;
  }

  // ── HEAD ──────────────────────────────────────────────────────────────────

  async headObject(realKey: string): Promise<Record<string, string>> {
    try {
      const resp = await this.client.send(
        new HeadObjectCommand({
          Bucket: this.bucket,
          Key: realKey,
        })
      );
      return {
        "content-type": resp.ContentType ?? "application/octet-stream",
        "content-length": String(resp.ContentLength ?? 0),
        etag: resp.ETag ?? "",
        "last-modified": resp.LastModified?.toUTCString() ?? "",
      };
    } catch (err) {
      throw this.mapError(err, realKey);
    }
  }

  // ── DELETE (single) ───────────────────────────────────────────────────────

  async deleteObject(realKey: string): Promise<void> {
    try {
      await this.client.send(
        new DeleteObjectCommand({
          Bucket: this.bucket,
          Key: realKey,
        })
      );
    } catch (err) {
      throw this.mapError(err, realKey);
    }
  }

  // ── DELETE (batch) via SDK ────────────────────────────────────────────────

  /**
   * Batch delete using `@aws-sdk/client-s3` DeleteObjectsCommand.
   * SDK handles chunking, XML assembly, and response parsing.
   *
   * Returns arrays of deleted keys and errors.
   */
  async deleteObjects(realKeys: string[]): Promise<{
    deleted: string[];
    errors: { key: string; message: string }[];
  }> {
    if (realKeys.length === 0) return { deleted: [], errors: [] };

    // S3 max per request is 1000 — chunk if needed
    const chunks = chunkArray(realKeys, 1000);
    const deleted: string[] = [];
    const errors: { key: string; message: string }[] = [];

    for (const chunk of chunks) {
      const objects: ObjectIdentifier[] = chunk.map((key) => ({ Key: key }));
      try {
        const resp = await this.client.send(
          new DeleteObjectsCommand({
            Bucket: this.bucket,
            Delete: { Objects: objects, Quiet: false },
          })
        );
        for (const d of resp.Deleted ?? []) {
          if (d.Key) deleted.push(d.Key);
        }
        for (const e of resp.Errors ?? []) {
          errors.push({
            key: e.Key ?? "",
            message: e.Message ?? "Unknown error",
          });
        }
      } catch (err) {
        // If the whole request fails, mark all keys in the chunk as errors
        const msg = err instanceof Error ? err.message : "DeleteObjects failed";
        errors.push(...chunk.map((key) => ({ key, message: msg })));
      }
    }
    return { deleted, errors };
  }

  // ── COPY (server-side) ────────────────────────────────────────────────────

  async copyObject(srcKey: string, dstKey: string): Promise<void> {
    try {
      await this.client.send(
        new CopyObjectCommand({
          Bucket: this.bucket,
          Key: dstKey,
          CopySource: `${this.bucket}/${srcKey}`,
        })
      );
    } catch (err) {
      throw this.mapError(err, dstKey);
    }
  }

  // ── LIST ──────────────────────────────────────────────────────────────────

  async listObjectsV2(params: {
    prefix: string;
    delimiter?: string;
    maxKeys: number;
    continuationToken?: string;
  }): Promise<ListObjectsV2CommandOutput> {
    try {
      return await this.client.send(
        new ListObjectsV2Command({
          Bucket: this.bucket,
          Prefix: params.prefix,
          Delimiter: params.delimiter,
          MaxKeys: params.maxKeys,
          ContinuationToken: params.continuationToken,
        })
      );
    } catch (err) {
      throw this.mapError(err, params.prefix);
    }
  }

  // ── MULTIPART ─────────────────────────────────────────────────────────────

  /** Initiate a multipart upload and return the backend UploadId. */
  async createMultipartUpload(
    realKey: string,
    contentType: string
  ): Promise<string> {
    try {
      const resp = await this.client.send(
        new CreateMultipartUploadCommand({
          Bucket: this.bucket,
          Key: realKey,
          ContentType: contentType,
        })
      );
      if (!resp.UploadId)
        throw ProxyError.internal("Backend returned no UploadId");
      return resp.UploadId;
    } catch (err) {
      throw this.mapError(err, realKey);
    }
  }

  /** Complete a multipart upload using the SDK's typed CompletedPart array. */
  async completeMultipartUpload(
    realKey: string,
    uploadId: string,
    parts: CompletedPart[]
  ): Promise<void> {
    try {
      await this.client.send(
        new CompleteMultipartUploadCommand({
          Bucket: this.bucket,
          Key: realKey,
          UploadId: uploadId,
          MultipartUpload: { Parts: parts },
        })
      );
    } catch (err) {
      throw this.mapError(err, realKey);
    }
  }

  /** Abort an in-progress multipart upload. */
  async abortMultipartUpload(realKey: string, uploadId: string): Promise<void> {
    try {
      await this.client.send(
        new AbortMultipartUploadCommand({
          Bucket: this.bucket,
          Key: realKey,
          UploadId: uploadId,
        })
      );
    } catch (err) {
      throw this.mapError(err, realKey);
    }
  }

  /** List all parts for an in-progress multipart upload. */
  async listParts(realKey: string, uploadId: string) {
    try {
      return await this.client.send(
        new ListPartsCommand({
          Bucket: this.bucket,
          Key: realKey,
          UploadId: uploadId,
        })
      );
    } catch (err) {
      throw this.mapError(err, realKey);
    }
  }

  /** Expose the underlying S3Client for presigned URL generation. */
  get rawClient(): S3Client {
    return this.client;
  }
  get bucketName(): string {
    return this.bucket;
  }

  // ─── Error mapping ─────────────────────────────────────────────────────────

  private mapError(err: unknown, key: string): ProxyError {
    if (err instanceof ProxyError) return err;
    if (err instanceof Error) {
      const name =
        (err as { Code?: string; name?: string }).Code ?? err.name ?? "";
      if (name === "NoSuchKey" || name === "NotFound")
        return ProxyError.noSuchKey(key);
      if (name === "NoSuchBucket") return ProxyError.noSuchBucket(this.bucket);
      if (name === "AccessDenied") return ProxyError.accessDenied();
      return ProxyError.internal(`${name}: ${err.message}`);
    }
    return ProxyError.internal(String(err));
  }
}

// ─── Helper ───────────────────────────────────────────────────────────────────

function chunkArray<T>(arr: T[], size: number): T[][] {
  const chunks: T[][] = [];
  for (let i = 0; i < arr.length; i += size) {
    chunks.push(arr.slice(i, i + size));
  }
  return chunks;
}
