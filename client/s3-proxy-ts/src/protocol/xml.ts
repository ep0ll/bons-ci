/**
 * S3 XML protocol layer.
 *
 * WHY XML: See docs/WHY_XML.md — the S3 wire protocol mandates XML for
 * both requests and responses. JSON is used internally (KV, admin) but
 * the public S3 API surface is XML-only to remain compatible with all
 * S3 clients.
 *
 * PARSING: `fast-xml-parser` — zero-allocation parser, no DOM tree,
 * no native deps. One shared `XMLParser` instance (module-level singleton)
 * is reused across all requests within an isolate.
 *
 * GENERATION: Hand-rolled string concatenation. S3 response schemas are
 * static and known at compile time; a library adds nothing and costs ~50 KB.
 * All values are XML-escaped before inclusion.
 */

import { XMLParser } from "fast-xml-parser";
import { xmlEscape } from "@/errors";

// ─── Parser singleton ─────────────────────────────────────────────────────────

/**
 * Shared `fast-xml-parser` instance.
 *
 * Configuration:
 * - `ignoreAttributes: false`    — needed for xmlns, versionId attrs
 * - `parseTagValue: false`       — we coerce types ourselves; avoids bugs
 *   where "true" becomes boolean and breaks Zod
 * - `trimValues: true`           — consistent whitespace handling
 * - `allowBooleanAttributes`     — handles <Quiet/> and <Quiet></Quiet>
 * - `isArray`                    — force arrays even for single elements;
 *   prevents the classic "one element → object, two → array" footgun
 * - `processEntities: true`      — decode &amp; &lt; etc. in key names
 */
const PARSER = new XMLParser({
  ignoreAttributes:     false,
  attributeNamePrefix:  "@_",
  parseTagValue:        false,
  trimValues:           true,
  allowBooleanAttributes: true,
  processEntities:      true,
  isArray: (tagName) => ["Object", "Part"].includes(tagName),
});

// ─── Incoming request parsing ─────────────────────────────────────────────────

export interface DeleteObjectEntry {
  Key:       string;
  VersionId?: string;
}

export interface DeleteRequest {
  quiet:   boolean;
  objects: DeleteObjectEntry[];
}

/** Parse a `<Delete>` body (DeleteObjects request). */
export function parseDeleteRequest(body: string): DeleteRequest {
  if (!body.trim()) throw new Error("Empty request body");

  let doc: unknown;
  try {
    doc = PARSER.parse(body);
  } catch (err) {
    throw new Error(`XML parse error: ${err instanceof Error ? err.message : String(err)}`);
  }

  const d = (doc as Record<string, unknown>)["Delete"];
  if (!d || typeof d !== "object") throw new Error("Missing <Delete> root element");
  const inner = d as Record<string, unknown>;

  const quietRaw  = inner["Quiet"];
  const quiet     = quietRaw === "true" || quietRaw === true;
  const objsRaw   = inner["Object"];
  const objArray  = Array.isArray(objsRaw) ? objsRaw : objsRaw ? [objsRaw] : [];

  const objects: DeleteObjectEntry[] = objArray.map((o: unknown, i) => {
    const obj = o as Record<string, unknown>;
    const key = obj["Key"];
    if (typeof key !== "string" || !key) {
      throw new Error(`Object[${i}] is missing a valid <Key>`);
    }
    return {
      Key:       key,
      VersionId: typeof obj["VersionId"] === "string" ? obj["VersionId"] : undefined,
    };
  });

  return { quiet, objects };
}

export interface CompletePart {
  PartNumber: number;
  ETag:       string;
}

export interface CompleteMpuRequest {
  parts: CompletePart[];
}

/** Parse a `<CompleteMultipartUpload>` body. */
export function parseCompleteMpu(body: string): CompleteMpuRequest {
  if (!body.trim()) throw new Error("Empty request body");

  let doc: unknown;
  try {
    doc = PARSER.parse(body);
  } catch (err) {
    throw new Error(`XML parse error: ${err instanceof Error ? err.message : String(err)}`);
  }

  const root = (doc as Record<string, unknown>)["CompleteMultipartUpload"];
  if (!root || typeof root !== "object") {
    throw new Error("Missing <CompleteMultipartUpload> root element");
  }
  const inner    = root as Record<string, unknown>;
  const partsRaw = inner["Part"];
  const partsArr = Array.isArray(partsRaw) ? partsRaw : partsRaw ? [partsRaw] : [];

  const parts: CompletePart[] = partsArr.map((p: unknown, i) => {
    const part = p as Record<string, unknown>;
    const pnRaw = part["PartNumber"];
    const pn    = typeof pnRaw === "string" ? parseInt(pnRaw, 10) : Number(pnRaw);
    if (!Number.isInteger(pn) || pn < 1 || pn > 10_000) {
      throw new Error(`Part[${i}] has invalid PartNumber: ${pnRaw}`);
    }
    const etag = part["ETag"];
    if (typeof etag !== "string" || !etag) {
      throw new Error(`Part[${i}] has missing or invalid ETag`);
    }
    return { PartNumber: pn, ETag: etag };
  });

  return { parts };
}

// ─── Response XML generation ──────────────────────────────────────────────────

const PREAMBLE = `<?xml version="1.0" encoding="UTF-8"?>\n`;
const NS       = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`;

// Helper: generate one XML element with a text value
const el = (tag: string, value: string, indent = "  "): string =>
  `${indent}<${tag}>${xmlEscape(value)}</${tag}>\n`;

// ── ListBuckets ───────────────────────────────────────────────────────────────

export function xmlListBuckets(
  ownerId:     string,
  displayName: string,
  buckets:     { name: string; createdAt: string }[],
): string {
  let s = `${PREAMBLE}<ListAllMyBucketsResult ${NS}>\n`;
  s    += `  <Owner>\n`;
  s    += el("ID",          ownerId,     "    ");
  s    += el("DisplayName", displayName, "    ");
  s    += `  </Owner>\n`;
  s    += `  <Buckets>\n`;
  for (const b of buckets) {
    s  += `    <Bucket>\n`;
    s  += el("Name",         b.name,      "      ");
    s  += el("CreationDate", b.createdAt, "      ");
    s  += `    </Bucket>\n`;
  }
  s    += `  </Buckets>\n`;
  s    += `</ListAllMyBucketsResult>`;
  return s;
}

// ── ListObjectsV2 ─────────────────────────────────────────────────────────────

export interface ListV2Params {
  bucket:                string;
  prefix:                string;
  delimiter:             string;
  maxKeys:               number;
  isTruncated:           boolean;
  keyCount:              number;
  nextContinuationToken?: string;
  continuationToken?:    string;
  startAfter?:           string;
  contents: {
    key: string; lastModified: string; etag: string;
    size: bigint; storageClass: string;
  }[];
  commonPrefixes: string[];
}

export function xmlListObjectsV2(p: ListV2Params): string {
  let s = `${PREAMBLE}<ListBucketResult ${NS}>\n`;
  s    += el("Name",        p.bucket);
  s    += el("Prefix",      p.prefix);
  s    += el("MaxKeys",     String(p.maxKeys));
  s    += el("KeyCount",    String(p.keyCount));
  s    += el("IsTruncated", p.isTruncated ? "true" : "false");
  if (p.delimiter)             s += el("Delimiter",              p.delimiter);
  if (p.continuationToken)     s += el("ContinuationToken",     p.continuationToken);
  if (p.nextContinuationToken) s += el("NextContinuationToken", p.nextContinuationToken);
  if (p.startAfter)            s += el("StartAfter",            p.startAfter);
  for (const o of p.contents) {
    s += `  <Contents>\n`;
    s += el("Key",          o.key,          "    ");
    s += el("LastModified", o.lastModified,  "    ");
    s += el("ETag",         o.etag,          "    ");
    s += `    <Size>${o.size}</Size>\n`;
    s += el("StorageClass", o.storageClass || "STANDARD", "    ");
    s += `  </Contents>\n`;
  }
  for (const cp of p.commonPrefixes) {
    s += `  <CommonPrefixes>\n    <Prefix>${xmlEscape(cp)}</Prefix>\n  </CommonPrefixes>\n`;
  }
  s    += `</ListBucketResult>`;
  return s;
}

// ── ListObjectsV1 ─────────────────────────────────────────────────────────────

export interface ListV1Params {
  bucket:      string;
  prefix:      string;
  delimiter:   string;
  marker:      string;
  maxKeys:     number;
  isTruncated: boolean;
  nextMarker?: string;
  contents: {
    key: string; lastModified: string; etag: string;
    size: bigint; storageClass: string;
  }[];
  commonPrefixes: string[];
}

export function xmlListObjectsV1(p: ListV1Params): string {
  let s = `${PREAMBLE}<ListBucketResult ${NS}>\n`;
  s    += el("Name",        p.bucket);
  s    += el("Prefix",      p.prefix);
  s    += el("Marker",      p.marker);
  s    += el("MaxKeys",     String(p.maxKeys));
  s    += el("IsTruncated", p.isTruncated ? "true" : "false");
  if (p.delimiter)  s += el("Delimiter",  p.delimiter);
  if (p.nextMarker) s += el("NextMarker", p.nextMarker);
  for (const o of p.contents) {
    s += `  <Contents>\n`;
    s += el("Key",          o.key,         "    ");
    s += el("LastModified", o.lastModified, "    ");
    s += el("ETag",         o.etag,         "    ");
    s += `    <Size>${o.size}</Size>\n`;
    s += el("StorageClass", o.storageClass || "STANDARD", "    ");
    s += `  </Contents>\n`;
  }
  for (const cp of p.commonPrefixes) {
    s += `  <CommonPrefixes>\n    <Prefix>${xmlEscape(cp)}</Prefix>\n  </CommonPrefixes>\n`;
  }
  s    += `</ListBucketResult>`;
  return s;
}

// ── Multipart ─────────────────────────────────────────────────────────────────

export function xmlCreateMpu(bucket: string, key: string, uploadId: string): string {
  let s = `${PREAMBLE}<InitiateMultipartUploadResult ${NS}>\n`;
  s    += el("Bucket",   bucket);
  s    += el("Key",      key);
  s    += el("UploadId", uploadId);
  s    += `</InitiateMultipartUploadResult>`;
  return s;
}

export function xmlCompleteResult(bucket: string, key: string, etag: string, location: string): string {
  let s = `${PREAMBLE}<CompleteMultipartUploadResult ${NS}>\n`;
  s    += el("Location", location || `/${bucket}/${key}`);
  s    += el("Bucket",   bucket);
  s    += el("Key",      key);
  s    += el("ETag",     etag);
  s    += `</CompleteMultipartUploadResult>`;
  return s;
}

export function xmlListParts(
  bucket:   string,
  key:      string,
  uploadId: string,
  isTruncated: boolean,
  nextPartNumber: number | undefined,
  parts: { partNumber: number; etag: string; size: bigint; lastModified: string }[],
): string {
  let s = `${PREAMBLE}<ListPartsResult ${NS}>\n`;
  s    += el("Bucket",      bucket);
  s    += el("Key",         key);
  s    += el("UploadId",    uploadId);
  s    += el("IsTruncated", isTruncated ? "true" : "false");
  if (nextPartNumber !== undefined) {
    s  += el("NextPartNumberMarker", String(nextPartNumber));
  }
  for (const p of parts) {
    s  += `  <Part>\n`;
    s  += el("PartNumber",   String(p.partNumber), "    ");
    s  += el("LastModified", p.lastModified,        "    ");
    s  += el("ETag",         p.etag,                "    ");
    s  += `    <Size>${p.size}</Size>\n`;
    s  += `  </Part>\n`;
  }
  s    += `</ListPartsResult>`;
  return s;
}

// ── DeleteObjects result ──────────────────────────────────────────────────────

export function xmlDeleteResult(
  deleted: string[],
  errors:  { key: string; code: string; message: string }[],
): string {
  let s = `${PREAMBLE}<DeleteResult ${NS}>\n`;
  for (const k of deleted) {
    s += `  <Deleted><Key>${xmlEscape(k)}</Key></Deleted>\n`;
  }
  for (const e of errors) {
    s += `  <Error>\n`;
    s += el("Key",     e.key,     "    ");
    s += el("Code",    e.code,    "    ");
    s += el("Message", e.message, "    ");
    s += `  </Error>\n`;
  }
  s    += `</DeleteResult>`;
  return s;
}

// ── CopyObject result ─────────────────────────────────────────────────────────

export function xmlCopyObjectResult(etag: string, lastModified: string): string {
  let s = `${PREAMBLE}<CopyObjectResult ${NS}>\n`;
  s    += el("ETag",         etag);
  s    += el("LastModified", lastModified);
  s    += `</CopyObjectResult>`;
  return s;
}
