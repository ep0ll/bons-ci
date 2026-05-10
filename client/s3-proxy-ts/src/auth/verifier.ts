/**
 * SigV4 verification using the official `@smithy/signature-v4` library.
 *
 * We use the library's `sign()` method to compute what the correct signature
 * _should_ be for the incoming request, then compare it in constant time
 * against the client-supplied signature.
 *
 * This replaces ~300 lines of hand-rolled SigV4 in v1/v2 with ~60 lines
 * that delegate all canonical-request, HMAC-SHA256, and string-to-sign
 * construction to the battle-tested Smithy implementation.
 *
 * @see https://www.npmjs.com/package/@smithy/signature-v4
 */
import { SignatureV4 } from "@smithy/signature-v4";
import { Sha256 } from "@aws-crypto/sha256-js";
import { constantTimeEqual } from "@/crypto";
import { ProxyError } from "@/errors";

// ─── Parsed Authorization header ─────────────────────────────────────────────

export interface ParsedAuth {
  readonly accessKeyId: string;
  readonly date: string; // YYYYMMDD
  readonly region: string;
  readonly service: string;
  readonly signedHeaders: string[];
  readonly signature: string;
}

export function parseAuthHeader(header: string): ParsedAuth {
  const tail = header.replace(/^AWS4-HMAC-SHA256\s+/, "");
  if (tail === header)
    throw ProxyError.malformedAuth("must start with AWS4-HMAC-SHA256");

  const parts = Object.fromEntries(
    tail.split(", ").map((p) => p.split("=", 2) as [string, string])
  );

  const credential = parts["Credential"] ?? "";
  const signedHeaders = parts["SignedHeaders"] ?? "";
  const signature = parts["Signature"] ?? "";

  if (!credential || !signedHeaders || !signature) {
    throw ProxyError.malformedAuth(
      "missing Credential, SignedHeaders, or Signature"
    );
  }

  const [akid, date, region, service, terminator] = credential.split("/");
  if (terminator !== "aws4_request" || !akid || !date || !region || !service) {
    throw ProxyError.malformedAuth(`invalid Credential: ${credential}`);
  }

  return {
    accessKeyId: akid,
    date,
    region,
    service,
    signedHeaders: signedHeaders.split(";"),
    signature,
  };
}

// ─── Verify ───────────────────────────────────────────────────────────────────

export interface VerifyParams {
  readonly method: string;
  readonly url: URL;
  readonly headers: Headers;
  readonly parsedAuth: ParsedAuth;
  /** Tenant's plaintext secret key (fetched from KV after access key lookup). */
  readonly secretKey: string;
}

/**
 * Verify an incoming SigV4 signature.
 *
 * Uses `@smithy/signature-v4` to reconstruct the expected signature, then
 * compares it against the client-supplied value in constant time.
 */
export async function verifySigV4(params: VerifyParams): Promise<void> {
  const { method, url, headers, parsedAuth, secretKey } = params;

  // Validate timestamp freshness (±15 minutes from now, per AWS spec)
  const amzDate = headers.get("x-amz-date") ?? "";
  if (!isTimestampFresh(amzDate)) throw ProxyError.requestExpired();

  // Build a request object in the shape @smithy/signature-v4 expects
  const requestToSign = {
    method,
    headers: headersToRecord(headers, parsedAuth.signedHeaders),
    hostname: url.hostname,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams),
    // Pass the payload hash from the header — "UNSIGNED-PAYLOAD" for presigned
    body: undefined as undefined,
  };

  const signer = new SignatureV4({
    credentials: {
      accessKeyId: parsedAuth.accessKeyId,
      secretAccessKey: secretKey,
    },
    region: parsedAuth.region,
    service: parsedAuth.service,
    sha256: Sha256,
    applyChecksum: false, // we accept UNSIGNED-PAYLOAD
  });

  // Re-sign the request to get the expected Authorization header
  const signed = await signer.sign(requestToSign, {
    signingDate: parseSigV4Date(parsedAuth.date, amzDate),
  });

  const expectedAuth = (signed.headers["authorization"] as string) ?? "";
  const expectedSig = extractSignature(expectedAuth);

  if (!constantTimeEqual(expectedSig, parsedAuth.signature)) {
    throw ProxyError.signatureMismatch();
  }
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

/**
 * Convert a Headers object to a plain record, only including headers
 * that appear in the signed headers list.
 */
function headersToRecord(
  headers: Headers,
  signedHeaderNames: string[]
): Record<string, string> {
  const result: Record<string, string> = {};
  for (const name of signedHeaderNames) {
    const value = headers.get(name);
    if (value !== null) result[name] = value;
  }
  return result;
}

/**
 * Parse an AMZ date string ("20240115T120000Z") into a Date object.
 * Falls back to the YYYYMMDD date if the full timestamp is unavailable.
 */
function parseSigV4Date(yyyymmdd: string, timestamp: string): Date {
  if (timestamp && /^\d{8}T\d{6}Z$/.test(timestamp)) {
    return new Date(
      `${timestamp.slice(0, 4)}-${timestamp.slice(4, 6)}-${timestamp.slice(
        6,
        8
      )}T` +
        `${timestamp.slice(9, 11)}:${timestamp.slice(11, 13)}:${timestamp.slice(
          13,
          15
        )}Z`
    );
  }
  return new Date(
    `${yyyymmdd.slice(0, 4)}-${yyyymmdd.slice(4, 6)}-${yyyymmdd.slice(
      6,
      8
    )}T00:00:00Z`
  );
}

/** Extract the Signature= value from an Authorization header. */
function extractSignature(auth: string): string {
  const match = /Signature=([0-9a-f]+)/i.exec(auth);
  return match?.[1] ?? "";
}

/** Check that an AMZ timestamp is within ±15 minutes of now. */
function isTimestampFresh(amzDate: string): boolean {
  if (!amzDate || !/^\d{8}T\d{6}Z$/.test(amzDate)) return false;

  const ts = parseSigV4Date(amzDate.slice(0, 8), amzDate).getTime();
  const now = Date.now();
  const FIFTEEN_MIN_MS = 15 * 60 * 1000;

  return Math.abs(now - ts) <= FIFTEEN_MIN_MS;
}
