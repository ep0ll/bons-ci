/**
 * S3 XML — parsing incoming requests with `fast-xml-parser` and
 * generating outgoing responses.
 *
 * `fast-xml-parser` replaces all hand-rolled `indexOf("<Tag>")` substring
 * scanning with a proper, allocating-once XML parser.  It compiles to ~50 KB
 * gzipped and has zero native dependencies.
 *
 * @see https://www.npmjs.com/package/fast-xml-parser
 */
import { XMLParser } from "fast-xml-parser";
import { xmlEscape } from "@/errors";

// ─── Shared parser instance (reused across requests in one isolate) ───────────

/**
 * `fast-xml-parser` instance configured for S3 request bodies.
 * - `ignoreAttributes: false` — needed for versionId attributes
 * - `parseTagValue: false`    — we do our own number coercion
 * - `trimValues: true`        — strip whitespace from text nodes
 * - `allowBooleanAttributes`  — handles <Quiet/> style tags
 */
const PARSER = new XMLParser({
  ignoreAttributes: false,
  parseTagValue: false, // keep strings; we coerce ourselves
  trimValues: true,
  allowBooleanAttributes: true,
  isArray: (tagName) =>
    // These elements always appear as arrays even when there is only one
    ["Object", "Part", "Delete"].includes(tagName),
});

// ─── Incoming request parsing ─────────────────────────────────────────────────

export interface DeleteObjectEntry {
  Key: string;
}

export interface DeleteRequest {
  Quiet: boolean;
  Objects: DeleteObjectEntry[];
}

/** Parse a `<Delete>` XML body (DeleteObjects request). */
export function parseDeleteRequest(xml: string): DeleteRequest {
  const doc = PARSER.parse(xml) as {
    Delete?: { Quiet?: string; Object?: { Key: string }[] };
  };

  const inner = doc.Delete;
  if (!inner) throw new Error("Missing <Delete> root element");

  return {
    Quiet: inner.Quiet === "true",
    Objects: (inner.Object ?? []).map((o) => ({ Key: o.Key })),
  };
}

export interface CompletePart {
  PartNumber: number;
  ETag: string;
}
export interface CompleteMpuRequest {
  Parts: CompletePart[];
}

/** Parse a `<CompleteMultipartUpload>` XML body. */
export function parseCompleteMpu(xml: string): CompleteMpuRequest {
  const doc = PARSER.parse(xml) as {
    CompleteMultipartUpload?: { Part?: { PartNumber: string; ETag: string }[] };
  };

  const inner = doc.CompleteMultipartUpload;
  if (!inner) throw new Error("Missing <CompleteMultipartUpload> root element");

  const parts = (inner.Part ?? []).map((p) => {
    const pn = parseInt(p.PartNumber, 10);
    if (isNaN(pn) || pn < 1 || pn > 10000) {
      throw new Error(`Invalid PartNumber: ${p.PartNumber}`);
    }
    return { PartNumber: pn, ETag: p.ETag };
  });

  return { Parts: parts };
}

// ─── Response XML generation ──────────────────────────────────────────────────

const PREAMBLE = `<?xml version="1.0" encoding="UTF-8"?>\n`;
const NS = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`;

function tag(name: string, value: string, indent = "  "): string {
  return `${indent}<${name}>${xmlEscape(value)}</${name}>\n`;
}

// ── ListBuckets ───────────────────────────────────────────────────────────────

export function xmlListBuckets(
  ownerId: string,
  buckets: { name: string; createdAt: string }[]
): string {
  let s = `${PREAMBLE}<ListAllMyBucketsResult ${NS}>\n`;
  s += `  <Owner><ID>${xmlEscape(ownerId)}</ID></Owner>\n`;
  s += `  <Buckets>\n`;
  for (const b of buckets) {
    s += `    <Bucket><Name>${xmlEscape(b.name)}</Name>`;
    s += `<CreationDate>${xmlEscape(b.createdAt)}</CreationDate></Bucket>\n`;
  }
  s += `  </Buckets>\n</ListAllMyBucketsResult>`;
  return s;
}

// ── ListObjectsV2 ─────────────────────────────────────────────────────────────

export interface ListEntry {
  key: string;
  lastModified: string;
  etag: string;
  size: number;
  storageClass: string;
}

export interface ListV2Params {
  bucket: string;
  prefix: string;
  delimiter: string;
  maxKeys: number;
  isTruncated: boolean;
  keyCount: number;
  nextContinuationToken?: string;
  continuationToken?: string;
  startAfter?: string;
  contents: ListEntry[];
  commonPrefixes: string[];
}

export function xmlListObjectsV2(p: ListV2Params): string {
  let s = `${PREAMBLE}<ListBucketResult ${NS}>\n`;
  s += tag("Name", p.bucket);
  s += tag("Prefix", p.prefix);
  s += tag("MaxKeys", String(p.maxKeys));
  s += tag("KeyCount", String(p.keyCount));
  s += tag("IsTruncated", p.isTruncated ? "true" : "false");
  if (p.delimiter) s += tag("Delimiter", p.delimiter);
  if (p.continuationToken) s += tag("ContinuationToken", p.continuationToken);
  if (p.nextContinuationToken)
    s += tag("NextContinuationToken", p.nextContinuationToken);
  if (p.startAfter) s += tag("StartAfter", p.startAfter);
  for (const o of p.contents) {
    s += `  <Contents>\n`;
    s += tag("Key", o.key, "    ");
    s += tag("LastModified", o.lastModified, "    ");
    s += tag("ETag", o.etag, "    ");
    s += `    <Size>${o.size}</Size>\n`;
    s += tag("StorageClass", o.storageClass || "STANDARD", "    ");
    s += `  </Contents>\n`;
  }
  for (const cp of p.commonPrefixes) {
    s += `  <CommonPrefixes><Prefix>${xmlEscape(
      cp
    )}</Prefix></CommonPrefixes>\n`;
  }
  s += `</ListBucketResult>`;
  return s;
}

// ── ListObjectsV1 ─────────────────────────────────────────────────────────────

export interface ListV1Params {
  bucket: string;
  prefix: string;
  delimiter: string;
  marker: string;
  maxKeys: number;
  isTruncated: boolean;
  nextMarker?: string;
  contents: ListEntry[];
  commonPrefixes: string[];
}

export function xmlListObjectsV1(p: ListV1Params): string {
  let s = `${PREAMBLE}<ListBucketResult ${NS}>\n`;
  s += tag("Name", p.bucket);
  s += tag("Prefix", p.prefix);
  s += tag("Marker", p.marker);
  s += tag("MaxKeys", String(p.maxKeys));
  s += tag("IsTruncated", p.isTruncated ? "true" : "false");
  if (p.delimiter) s += tag("Delimiter", p.delimiter);
  if (p.nextMarker) s += tag("NextMarker", p.nextMarker);
  for (const o of p.contents) {
    s += `  <Contents>\n`;
    s += tag("Key", o.key, "    ");
    s += tag("LastModified", o.lastModified, "    ");
    s += tag("ETag", o.etag, "    ");
    s += `    <Size>${o.size}</Size>\n`;
    s += tag("StorageClass", o.storageClass || "STANDARD", "    ");
    s += `  </Contents>\n`;
  }
  for (const cp of p.commonPrefixes) {
    s += `  <CommonPrefixes><Prefix>${xmlEscape(
      cp
    )}</Prefix></CommonPrefixes>\n`;
  }
  s += `</ListBucketResult>`;
  return s;
}

// ── Multipart responses ───────────────────────────────────────────────────────

export function xmlCreateMpu(
  bucket: string,
  key: string,
  uploadId: string
): string {
  let s = `${PREAMBLE}<InitiateMultipartUploadResult ${NS}>\n`;
  s += tag("Bucket", bucket);
  s += tag("Key", key);
  s += tag("UploadId", uploadId);
  s += `</InitiateMultipartUploadResult>`;
  return s;
}

export function xmlListParts(
  bucket: string,
  key: string,
  uploadId: string,
  parts: {
    partNumber: number;
    etag: string;
    size: number;
    lastModified: string;
  }[]
): string {
  let s = `${PREAMBLE}<ListPartsResult ${NS}>\n`;
  s += tag("Bucket", bucket);
  s += tag("Key", key);
  s += tag("UploadId", uploadId);
  for (const p of parts) {
    s += `  <Part>\n`;
    s += `    <PartNumber>${p.partNumber}</PartNumber>\n`;
    s += tag("LastModified", p.lastModified, "    ");
    s += tag("ETag", p.etag, "    ");
    s += `    <Size>${p.size}</Size>\n`;
    s += `  </Part>\n`;
  }
  s += `</ListPartsResult>`;
  return s;
}

// ── DeleteObjects response ────────────────────────────────────────────────────

export function xmlDeleteResult(
  deleted: string[],
  errors: { key: string; message: string }[]
): string {
  let s = `${PREAMBLE}<DeleteResult ${NS}>\n`;
  for (const k of deleted) {
    s += `  <Deleted><Key>${xmlEscape(k)}</Key></Deleted>\n`;
  }
  for (const e of errors) {
    s += `  <Error><Key>${xmlEscape(e.key)}</Key><Message>${xmlEscape(
      e.message
    )}</Message></Error>\n`;
  }
  s += `</DeleteResult>`;
  return s;
}
