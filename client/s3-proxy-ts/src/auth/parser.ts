/**
 * SigV4 Authorization header parsing.
 *
 * Handles both auth styles the AWS SDK uses:
 *   1. Header style: `Authorization: AWS4-HMAC-SHA256 Credential=…`
 *   2. Query-string presigned: `?X-Amz-Credential=…&X-Amz-Signature=…`
 *
 * Parsing is strict — any deviation from the spec throws a typed ProxyError
 * that maps to the exact AWS SDK error the client expects.
 */

import { ProxyError } from "@/errors";

// ─── Parsed auth ──────────────────────────────────────────────────────────────

export interface ParsedAuth {
  readonly accessKeyId:   string;
  readonly date:          string;   // YYYYMMDD
  readonly region:        string;
  readonly service:       string;
  readonly signedHeaders: readonly string[];
  readonly signature:     string;
  readonly timestamp:     string;   // YYYYMMDDTHHmmssZ (from x-amz-date or X-Amz-Date)
  readonly expiresAt:     Date | null; // null for header auth; Date for presigned
  readonly isPresigned:   boolean;
}

// ─── Header auth ──────────────────────────────────────────────────────────────

/**
 * Parse `Authorization: AWS4-HMAC-SHA256 Credential=…, SignedHeaders=…, Signature=…`
 *
 * Spec: https://docs.aws.amazon.com/general/latest/gr/sigv4-create-string-to-sign.html
 */
export function parseAuthHeader(header: string, xAmzDate: string | null): ParsedAuth {
  if (!header.startsWith("AWS4-HMAC-SHA256 ")) {
    throw ProxyError.malformedAuth("Authorization must use AWS4-HMAC-SHA256 scheme");
  }

  const parts = header.slice("AWS4-HMAC-SHA256 ".length);

  // Split on ", " — the AWS spec uses ", " (comma space) as separator
  const fields = Object.fromEntries(
    parts.split(", ").map((p) => {
      const eq = p.indexOf("=");
      if (eq < 0) throw ProxyError.malformedAuth(`Malformed auth field: ${p}`);
      return [p.slice(0, eq).trim(), p.slice(eq + 1).trim()] as [string, string];
    }),
  );

  const credential    = fields["Credential"]    ?? "";
  const signedHeaders = fields["SignedHeaders"] ?? "";
  const signature     = fields["Signature"]     ?? "";

  if (!credential)    throw ProxyError.malformedAuth("Missing Credential field");
  if (!signedHeaders) throw ProxyError.malformedAuth("Missing SignedHeaders field");
  if (!signature)     throw ProxyError.malformedAuth("Missing Signature field");

  // Validate signature is lowercase hex
  if (!/^[0-9a-f]{64}$/i.test(signature)) {
    throw ProxyError.malformedAuth("Signature must be 64 lowercase hex characters");
  }

  const { accessKeyId, date, region, service } = parseCredential(credential);

  // x-amz-date is required for header-style auth
  const timestamp = xAmzDate?.trim() ?? "";
  if (!isValidAmzTimestamp(timestamp)) {
    throw ProxyError.malformedAuth(
      `x-amz-date is missing or malformed: ${timestamp}. Expected YYYYMMDDTHHmmssZ`,
    );
  }

  // Verify date in credential matches x-amz-date date portion
  if (timestamp.slice(0, 8) !== date) {
    throw ProxyError.malformedAuth(
      `Credential date (${date}) does not match x-amz-date date (${timestamp.slice(0, 8)})`,
    );
  }

  return {
    accessKeyId,
    date,
    region,
    service,
    signedHeaders:  signedHeaders.split(";").filter(Boolean),
    signature:      signature.toLowerCase(),
    timestamp,
    expiresAt:      null,
    isPresigned:    false,
  };
}

// ─── Presigned URL auth ───────────────────────────────────────────────────────

/**
 * Parse presigned URL query parameters:
 * `X-Amz-Algorithm`, `X-Amz-Credential`, `X-Amz-Date`,
 * `X-Amz-Expires`, `X-Amz-SignedHeaders`, `X-Amz-Signature`
 */
export function parsePresignedParams(params: URLSearchParams): ParsedAuth {
  const algorithm = params.get("X-Amz-Algorithm") ?? "";
  if (algorithm !== "AWS4-HMAC-SHA256") {
    throw ProxyError.malformedAuth(`Unsupported algorithm: ${algorithm}`);
  }

  const credential    = params.get("X-Amz-Credential") ?? "";
  const timestamp     = params.get("X-Amz-Date")       ?? "";
  const expiresStr    = params.get("X-Amz-Expires")    ?? "";
  const signedHeaders = params.get("X-Amz-SignedHeaders") ?? "";
  const signature     = params.get("X-Amz-Signature")  ?? "";

  if (!credential)    throw ProxyError.malformedAuth("Missing X-Amz-Credential");
  if (!timestamp)     throw ProxyError.malformedAuth("Missing X-Amz-Date");
  if (!expiresStr)    throw ProxyError.malformedAuth("Missing X-Amz-Expires");
  if (!signedHeaders) throw ProxyError.malformedAuth("Missing X-Amz-SignedHeaders");
  if (!signature)     throw ProxyError.malformedAuth("Missing X-Amz-Signature");

  if (!isValidAmzTimestamp(timestamp)) {
    throw ProxyError.malformedAuth(`X-Amz-Date malformed: ${timestamp}`);
  }

  const expires = parseInt(expiresStr, 10);
  if (isNaN(expires) || expires < 1 || expires > 604800) {
    throw ProxyError.malformedAuth(
      `X-Amz-Expires must be 1-604800 seconds, got: ${expiresStr}`,
    );
  }

  const { accessKeyId, date, region, service } = parseCredential(credential);

  // Compute absolute expiry time
  const issuedAt  = amzTimestampToDate(timestamp);
  const expiresAt = new Date(issuedAt.getTime() + expires * 1000);

  return {
    accessKeyId,
    date,
    region,
    service,
    signedHeaders: signedHeaders.split(";").filter(Boolean),
    signature:     signature.toLowerCase(),
    timestamp,
    expiresAt,
    isPresigned: true,
  };
}

// ─── Credential scope ────────────────────────────────────────────────────────

function parseCredential(credential: string): {
  accessKeyId: string;
  date: string;
  region: string;
  service: string;
} {
  // Format: AKID/YYYYMMDD/region/service/aws4_request
  const parts = credential.split("/");
  if (parts.length !== 5 || parts[4] !== "aws4_request") {
    throw ProxyError.malformedAuth(
      `Invalid Credential format. Expected AKID/YYYYMMDD/region/service/aws4_request, got: ${credential}`,
    );
  }

  const [accessKeyId, date, region, service] = parts as [string, string, string, string, string];

  if (!accessKeyId) throw ProxyError.malformedAuth("Access key ID is empty");
  if (!/^\d{8}$/.test(date)) {
    throw ProxyError.malformedAuth(`Credential date must be YYYYMMDD, got: ${date}`);
  }
  if (!region)  throw ProxyError.malformedAuth("Region is empty in Credential");
  if (!service) throw ProxyError.malformedAuth("Service is empty in Credential");

  return { accessKeyId, date, region, service };
}

// ─── Timestamp helpers ────────────────────────────────────────────────────────

/** Validate "YYYYMMDDTHHmmssZ" format. */
export function isValidAmzTimestamp(ts: string): boolean {
  return /^\d{8}T\d{6}Z$/.test(ts);
}

/** Parse "YYYYMMDDTHHmmssZ" → Date. */
export function amzTimestampToDate(ts: string): Date {
  // "20240115T120000Z" → "2024-01-15T12:00:00Z"
  const iso = `${ts.slice(0, 4)}-${ts.slice(4, 6)}-${ts.slice(6, 8)}T` +
              `${ts.slice(9, 11)}:${ts.slice(11, 13)}:${ts.slice(13, 15)}Z`;
  const d = new Date(iso);
  if (isNaN(d.getTime())) throw ProxyError.malformedAuth(`Cannot parse timestamp: ${ts}`);
  return d;
}

/** Check that a timestamp is within ±15 minutes of now (per AWS spec). */
export function isTimestampFresh(timestamp: string): boolean {
  try {
    const ts  = amzTimestampToDate(timestamp).getTime();
    const now = Date.now();
    return Math.abs(now - ts) <= 15 * 60 * 1000;
  } catch {
    return false;
  }
}
