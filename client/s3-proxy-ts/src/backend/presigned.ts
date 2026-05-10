/**
 * Presigned URL generation using `@aws-sdk/s3-request-presigner`.
 *
 * This is the highest-leverage library in the stack: one `getSignedUrl()` call
 * replaces ~150 lines of hand-rolled SigV4 query-string signing from v1/v2.
 * The library handles:
 *   - SigV4 query-string parameter assembly and sorting
 *   - Canonical request construction
 *   - String-to-sign computation
 *   - HMAC-SHA256 key derivation and signature computation
 *   - URL encoding of all parameters
 *
 * Works with B2, S3, and MinIO because they all accept standard SigV4
 * presigned URLs.
 *
 * @see https://www.npmjs.com/package/@aws-sdk/s3-request-presigner
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

export interface PresignOptions {
  /** URL lifetime in seconds. Default: 900 (15 min, matches AWS SDK default). */
  expiresIn?: number;
}

const DEFAULT_EXPIRES = 900;

// ─── Presigned URL generators ─────────────────────────────────────────────────

/**
 * Generate a presigned GET URL for object download.
 * The client follows this URL and downloads directly from the backend.
 */
export async function presignGetObject(
  client: S3Client,
  bucket: string,
  key: string,
  opts?: PresignOptions
): Promise<string> {
  return getSignedUrl(
    client,
    new GetObjectCommand({ Bucket: bucket, Key: key }),
    { expiresIn: opts?.expiresIn ?? DEFAULT_EXPIRES }
  );
}

/**
 * Generate a presigned PUT URL for object upload.
 * The client follows this 307 redirect and uploads directly to the backend.
 * Works with AWS S3, Backblaze B2, and MinIO.
 */
export async function presignPutObject(
  client: S3Client,
  bucket: string,
  key: string,
  opts?: PresignOptions & {
    contentType?: string;
    metadata?: Record<string, string>;
  }
): Promise<string> {
  return getSignedUrl(
    client,
    new PutObjectCommand({
      Bucket: bucket,
      Key: key,
      ContentType: opts?.contentType,
      Metadata: opts?.metadata,
    }),
    { expiresIn: opts?.expiresIn ?? DEFAULT_EXPIRES }
  );
}

/**
 * Generate a presigned HEAD URL for metadata retrieval.
 */
export async function presignHeadObject(
  client: S3Client,
  bucket: string,
  key: string,
  opts?: PresignOptions
): Promise<string> {
  return getSignedUrl(
    client,
    new HeadObjectCommand({ Bucket: bucket, Key: key }),
    { expiresIn: opts?.expiresIn ?? DEFAULT_EXPIRES }
  );
}

/**
 * Generate a presigned PUT URL for a single multipart upload part.
 *
 * The client uploads the part directly to the backend, gets back the ETag,
 * and passes it to CompleteMultipartUpload.
 */
export async function presignUploadPart(
  client: S3Client,
  bucket: string,
  key: string,
  uploadId: string,
  partNumber: number,
  opts?: PresignOptions
): Promise<string> {
  return getSignedUrl(
    client,
    new UploadPartCommand({
      Bucket: bucket,
      Key: key,
      UploadId: uploadId,
      PartNumber: partNumber,
    }),
    { expiresIn: opts?.expiresIn ?? DEFAULT_EXPIRES }
  );
}

/**
 * Generate a presigned DELETE URL.
 * Rarely needed (DELETE is typically proxied directly), but available for
 * clients that want to issue deletes client-side.
 */
export async function presignDeleteObject(
  client: S3Client,
  bucket: string,
  key: string,
  opts?: PresignOptions
): Promise<string> {
  return getSignedUrl(
    client,
    new DeleteObjectCommand({ Bucket: bucket, Key: key }),
    { expiresIn: opts?.expiresIn ?? DEFAULT_EXPIRES }
  );
}
