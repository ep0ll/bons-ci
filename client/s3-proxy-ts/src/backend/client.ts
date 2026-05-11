/**
 * Backend S3-compatible client for control-plane operations.
 *
 * Wraps `@aws-sdk/client-s3` so that the same code works with
 * Backblaze B2, Amazon S3, MinIO, and Cloudflare R2.
 *
 * Per-request clients are created cheaply — the AWS SDK uses fetch()
 * internally (no persistent HTTP connections in Workers), so there is
 * no pooling concern.
 *
 * Error mapping: all SDK errors (ServiceException subclasses) are
 * translated to typed `ProxyError` instances at this boundary.
 * Nothing above this module should handle raw SDK exceptions.
 */

import {
  S3Client,
  type S3ClientConfig,
  HeadObjectCommand,
  DeleteObjectCommand,
  DeleteObjectsCommand,
  CopyObjectCommand,
  ListObjectsV2Command,
  CreateMultipartUploadCommand,
  CompleteMultipartUploadCommand,
  AbortMultipartUploadCommand,
  ListPartsCommand,
  type ListObjectsV2CommandOutput,
  type CompletedPart,
  type ObjectIdentifier,
  type _Object,
  type Part,
} from "@aws-sdk/client-s3";

import { ProxyError }      from "@/errors";
import type { BackendConfig } from "@/tenant";

// ─── Client factory ───────────────────────────────────────────────────────────

/**
 * Build an `@aws-sdk/client-s3` instance for a specific backend config.
 *
 * Key decisions:
 * - `requestHandler` is not overridden — the SDK detects the Workers
 *   `fetch` global via `@smithy/fetch-http-handler` automatically when
 *   running in a non-Node environment.
 * - `useFipsEndpoint: false` — FIPS endpoints are not relevant for B2/MinIO.
 * - `disableHostPrefix: true` — prevents the SDK from prepending bucket
 *   names to the hostname (we use path-style or explicit endpoint).
 */
export function makeS3Client(config: BackendConfig, secretKey: string): S3Client {
  // Normalise the endpoint URL
  const endpoint = config.endpointUrl
    ?? (config.endpoint.startsWith("http")
      ? config.endpoint
      : `https://${config.endpoint}`);

  const cfg: S3ClientConfig = {
    region:      config.region,
    endpoint,
    credentials: {
      accessKeyId:     config.accessKeyId,
      secretAccessKey: secretKey,
    },
    forcePathStyle:     config.forcePathStyle,
    disableHostPrefix:  config.forcePathStyle, // prevents bucket-in-hostname
    useFipsEndpoint:    false,
    // Workers doesn't support request streaming to the SDK layer
    requestChecksumCalculation: "WHEN_REQUIRED",
    responseChecksumValidation: "WHEN_REQUIRED",
  };

  return new S3Client(cfg);
}

// ─── S3CompatClient ───────────────────────────────────────────────────────────

/** Typed result from `headObject`. */
export interface HeadObjectResult {
  contentType:   string;
  contentLength: bigint;
  etag:          string;
  lastModified:  Date;
  extraHeaders:  Record<string, string>;
}

/** A single object in a listing result. */
export interface ListedObject {
  key:          string;
  lastModified: Date;
  etag:         string;
  size:         bigint;
  storageClass: string;
}

/** Result from `listObjectsV2`. */
export interface ListResult {
  objects:               ListedObject[];
  commonPrefixes:        string[];
  isTruncated:           boolean;
  nextContinuationToken: string | undefined;
  keyCount:              number;
}

/** Result from `deleteObjects`. */
export interface BatchDeleteResult {
  deleted: string[];
  errors:  { key: string; code: string; message: string }[];
}

export class S3CompatClient {
  private readonly client: S3Client;
  readonly bucket: string;

  constructor(config: BackendConfig, secretKey: string) {
    this.client = makeS3Client(config, secretKey);
    this.bucket = config.bucket;
  }

  get rawClient(): S3Client { return this.client; }

  // ── HEAD ──────────────────────────────────────────────────────────────────

  async headObject(realKey: string): Promise<HeadObjectResult> {
    try {
      const r = await this.client.send(new HeadObjectCommand({
        Bucket: this.bucket,
        Key:    realKey,
      }));
      return {
        contentType:   r.ContentType   ?? "application/octet-stream",
        contentLength: BigInt(r.ContentLength ?? 0),
        etag:          r.ETag          ?? "",
        lastModified:  r.LastModified  ?? new Date(0),
        extraHeaders:  extractMetadata(r.Metadata),
      };
    } catch (err) {
      throw mapSdkError(err, realKey, this.bucket);
    }
  }

  // ── DELETE (single) ───────────────────────────────────────────────────────

  async deleteObject(realKey: string): Promise<void> {
    try {
      await this.client.send(new DeleteObjectCommand({
        Bucket: this.bucket,
        Key:    realKey,
      }));
    } catch (err) {
      throw mapSdkError(err, realKey, this.bucket);
    }
  }

  // ── DELETE (batch) ────────────────────────────────────────────────────────

  /**
   * Delete up to 1000 objects per call (SDK handles the XML body).
   * Automatically chunks arrays larger than 1000.
   */
  async deleteObjects(realKeys: string[]): Promise<BatchDeleteResult> {
    if (realKeys.length === 0) return { deleted: [], errors: [] };

    const allDeleted: string[] = [];
    const allErrors:  BatchDeleteResult["errors"] = [];

    for (const chunk of chunkArray(realKeys, 1000)) {
      const objects: ObjectIdentifier[] = chunk.map((k) => ({ Key: k }));
      try {
        const r = await this.client.send(new DeleteObjectsCommand({
          Bucket: this.bucket,
          Delete: { Objects: objects, Quiet: false },
        }));
        for (const d of r.Deleted ?? []) {
          if (d.Key) allDeleted.push(d.Key);
        }
        for (const e of r.Errors ?? []) {
          allErrors.push({
            key:     e.Key     ?? "",
            code:    e.Code    ?? "InternalError",
            message: e.Message ?? "Unknown error",
          });
        }
      } catch (err) {
        const msg = err instanceof Error ? err.message : "DeleteObjects failed";
        allErrors.push(...chunk.map((k) => ({ key: k, code: "InternalError", message: msg })));
      }
    }
    return { deleted: allDeleted, errors: allErrors };
  }

  // ── COPY ──────────────────────────────────────────────────────────────────

  async copyObject(
    srcKey: string,
    dstKey: string,
    opts?: {
      metadataDirective?: "COPY" | "REPLACE";
      contentType?:       string;
      metadata?:          Record<string, string>;
    },
  ): Promise<{ etag: string }> {
    try {
      const r = await this.client.send(new CopyObjectCommand({
        Bucket:            this.bucket,
        Key:               dstKey,
        CopySource:        encodeURIComponent(`${this.bucket}/${srcKey}`),
        MetadataDirective: opts?.metadataDirective ?? "COPY",
        ContentType:       opts?.contentType,
        Metadata:          opts?.metadata,
      }));
      return { etag: r.CopyObjectResult?.ETag ?? "" };
    } catch (err) {
      throw mapSdkError(err, dstKey, this.bucket);
    }
  }

  // ── LIST ──────────────────────────────────────────────────────────────────

  async listObjectsV2(params: {
    prefix:             string;
    delimiter?:         string;
    maxKeys:            number;
    continuationToken?: string;
    startAfter?:        string;
    fetchOwner?:        boolean;
  }): Promise<ListResult> {
    try {
      const r: ListObjectsV2CommandOutput = await this.client.send(
        new ListObjectsV2Command({
          Bucket:            this.bucket,
          Prefix:            params.prefix,
          Delimiter:         params.delimiter,
          MaxKeys:           params.maxKeys,
          ContinuationToken: params.continuationToken,
          StartAfter:        params.startAfter,
          FetchOwner:        params.fetchOwner,
        }),
      );

      const objects: ListedObject[] = (r.Contents ?? []).map((obj: _Object) => ({
        key:          obj.Key          ?? "",
        lastModified: obj.LastModified ?? new Date(0),
        etag:         obj.ETag         ?? "",
        size:         BigInt(obj.Size  ?? 0),
        storageClass: obj.StorageClass ?? "STANDARD",
      }));

      const commonPrefixes = (r.CommonPrefixes ?? [])
        .map((cp) => cp.Prefix ?? "")
        .filter(Boolean);

      return {
        objects,
        commonPrefixes,
        isTruncated:           r.IsTruncated                ?? false,
        nextContinuationToken: r.NextContinuationToken      ?? undefined,
        keyCount:              r.KeyCount                   ?? objects.length,
      };
    } catch (err) {
      throw mapSdkError(err, params.prefix, this.bucket);
    }
  }

  // ── MULTIPART ─────────────────────────────────────────────────────────────

  async createMultipartUpload(
    realKey:     string,
    contentType: string,
    metadata?:   Record<string, string>,
  ): Promise<string> {
    try {
      const r = await this.client.send(new CreateMultipartUploadCommand({
        Bucket:      this.bucket,
        Key:         realKey,
        ContentType: contentType,
        Metadata:    metadata,
      }));
      if (!r.UploadId) throw ProxyError.internal("Backend returned no UploadId");
      return r.UploadId;
    } catch (err) {
      throw mapSdkError(err, realKey, this.bucket);
    }
  }

  async completeMultipartUpload(
    realKey:  string,
    uploadId: string,
    parts:    CompletedPart[],
  ): Promise<{ etag: string; location: string }> {
    try {
      const r = await this.client.send(new CompleteMultipartUploadCommand({
        Bucket:          this.bucket,
        Key:             realKey,
        UploadId:        uploadId,
        MultipartUpload: { Parts: parts },
      }));
      return { etag: r.ETag ?? "", location: r.Location ?? "" };
    } catch (err) {
      throw mapSdkError(err, realKey, this.bucket);
    }
  }

  async abortMultipartUpload(realKey: string, uploadId: string): Promise<void> {
    try {
      await this.client.send(new AbortMultipartUploadCommand({
        Bucket:   this.bucket,
        Key:      realKey,
        UploadId: uploadId,
      }));
    } catch (err) {
      throw mapSdkError(err, realKey, this.bucket);
    }
  }

  async listParts(
    realKey:  string,
    uploadId: string,
    maxParts?: number,
    partNumberMarker?: number,
  ): Promise<{ parts: Part[]; isTruncated: boolean; nextPartNumber?: number }> {
    try {
      const r = await this.client.send(new ListPartsCommand({
        Bucket:           this.bucket,
        Key:              realKey,
        UploadId:         uploadId,
        MaxParts:         maxParts,
        PartNumberMarker: partNumberMarker ? String(partNumberMarker) : undefined,
      }));
      return {
        parts:           r.Parts           ?? [],
        isTruncated:     r.IsTruncated     ?? false,
        nextPartNumber:  r.NextPartNumberMarker ? Number(r.NextPartNumberMarker) : undefined,
      };
    } catch (err) {
      throw mapSdkError(err, realKey, this.bucket);
    }
  }
}

// ─── SDK error mapping ────────────────────────────────────────────────────────

function mapSdkError(err: unknown, key: string, bucket: string): ProxyError {
  if (err instanceof ProxyError) return err;

  if (err instanceof Error) {
    const code = (err as { Code?: string; $metadata?: { httpStatusCode?: number } }).Code
              ?? (err as { name?: string }).name
              ?? "";
    const status = (err as { $metadata?: { httpStatusCode?: number } })
      .$metadata?.httpStatusCode;

    // Map SDK error codes to ProxyError
    if (code === "NoSuchKey"   || status === 404) return ProxyError.noSuchKey(key);
    if (code === "NoSuchBucket") return ProxyError.noSuchBucket(bucket);
    if (code === "AccessDenied"  || status === 403) return ProxyError.accessDenied(err.message);
    if (code === "NoSuchUpload") return ProxyError.noSuchUpload(key);
    if (code === "SlowDown"      || status === 503) return ProxyError.slowDown(err.message);
    if (code === "InvalidPart")  return ProxyError.invalidPart(err.message);

    console.error(`Backend SDK error [${code}] status=${status}:`, err.message);
    return ProxyError.internal(`Backend error: ${err.message}`);
  }

  return ProxyError.internal(`Backend error: ${String(err)}`);
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function extractMetadata(metadata?: Record<string, string>): Record<string, string> {
  if (!metadata) return {};
  // SDK lowercases all metadata keys; re-prefix them for forwarding
  return Object.fromEntries(
    Object.entries(metadata).map(([k, v]) => [`x-amz-meta-${k}`, v]),
  );
}

function chunkArray<T>(arr: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < arr.length; i += size) out.push(arr.slice(i, i + size));
  return out;
}
