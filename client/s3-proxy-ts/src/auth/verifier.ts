/**
 * SigV4 signature verification using `@smithy/signature-v4`.
 *
 * We reconstruct the request the client signed, then ask the Smithy library
 * to compute what the correct signature should be. We compare the result
 * to the client-supplied signature using constant-time comparison.
 *
 * This handles all edge cases that hand-rolled SigV4 gets wrong:
 *   - Canonical header value normalisation (multi-space collapsing)
 *   - URI double-encoding rules for path segments
 *   - Query parameter sorting (encoded key, then encoded value)
 *   - Chunked transfer encoding detection
 *   - `UNSIGNED-PAYLOAD` vs actual SHA-256 payload hash
 *
 * Presigned URL verification also supported — the Smithy library
 * correctly handles the `X-Amz-*` query params being part of the
 * canonical request rather than headers.
 */

import { SignatureV4 }      from "@smithy/signature-v4";
import { Sha256 }           from "@aws-crypto/sha256-js";
import { constantTimeEqual } from "@/crypto";
import { ProxyError }        from "@/errors";
import {
  parseAuthHeader,
  parsePresignedParams,
  isTimestampFresh,
  type ParsedAuth,
} from "./parser";

// ─── Public API ───────────────────────────────────────────────────────────────

export { ParsedAuth };

export interface VerifyResult {
  readonly parsed: ParsedAuth;
}

/**
 * Verify the SigV4 signature on an incoming request.
 *
 * Supports both:
 *   - Header auth (`Authorization: AWS4-HMAC-SHA256 …`)
 *   - Presigned URL auth (`?X-Amz-Signature=…`)
 *
 * Returns the parsed auth fields for use in subsequent handler logic.
 */
export async function verifySigV4(
  request:   Request,
  secretKey: string,
): Promise<VerifyResult> {
  const url     = new URL(request.url);
  const headers = request.headers;

  // ── Detect auth style ──────────────────────────────────────────────────────
  const isPresigned = url.searchParams.has("X-Amz-Signature");

  let parsed: ParsedAuth;

  if (isPresigned) {
    parsed = parsePresignedParams(url.searchParams);

    // Presigned URL: check wall-clock expiry first
    if (!parsed.expiresAt || Date.now() > parsed.expiresAt.getTime()) {
      throw ProxyError.requestExpired();
    }
  } else {
    const authHeader  = headers.get("Authorization");
    if (!authHeader)  throw ProxyError.noAuth();

    const xAmzDate    = headers.get("x-amz-date");
    parsed            = parseAuthHeader(authHeader, xAmzDate);

    // Header auth: check timestamp freshness (±15 min)
    if (!isTimestampFresh(parsed.timestamp)) {
      throw ProxyError.requestExpired();
    }
  }

  // ── Build the HttpRequest the Smithy signer expects ───────────────────────
  const smithyRequest = buildSmithyRequest(request, url, parsed, isPresigned);

  // ── Re-sign using @smithy/signature-v4 ───────────────────────────────────
  const signer = new SignatureV4({
    credentials: {
      accessKeyId:     parsed.accessKeyId,
      secretAccessKey: secretKey,
    },
    region:  parsed.region,
    service: parsed.service,
    sha256:  Sha256,
    // applyChecksum: false allows UNSIGNED-PAYLOAD to pass through
    applyChecksum: false,
  });

  let expectedSig: string;
  try {
    if (isPresigned) {
      // For presigned URLs, use presign() to get the expected query params
      const signed = await signer.presign(smithyRequest, {
        expiresIn:   Math.ceil((parsed.expiresAt!.getTime() - new Date(parsed.timestamp.replace(
          /(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z/,
          "$1-$2-$3T$4:$5:$6Z",
        )).getTime()) / 1000),
        signingDate: new Date(parsed.timestamp.replace(
          /(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z/,
          "$1-$2-$3T$4:$5:$6Z",
        )),
      });
      expectedSig = (new URL(`https://x${signed.path ?? ""}`)).searchParams
        .get("X-Amz-Signature") ?? "";
    } else {
      const signed = await signer.sign(smithyRequest, {
        signingDate: new Date(parsed.timestamp.replace(
          /(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z/,
          "$1-$2-$3T$4:$5:$6Z",
        )),
      });
      const authValue = (signed.headers as Record<string, string>)["authorization"] ?? "";
      const match = /Signature=([0-9a-f]+)/i.exec(authValue);
      expectedSig = match?.[1]?.toLowerCase() ?? "";
    }
  } catch (err) {
    // Signer errors indicate malformed request rather than wrong credentials
    throw ProxyError.malformedAuth(
      `Cannot compute expected signature: ${err instanceof Error ? err.message : String(err)}`,
    );
  }

  if (!expectedSig) {
    throw ProxyError.internal("Signer produced empty signature");
  }

  // ── Constant-time comparison ──────────────────────────────────────────────
  if (!constantTimeEqual(expectedSig, parsed.signature.toLowerCase())) {
    throw ProxyError.signatureMismatch();
  }

  return { parsed };
}

// ─── Smithy request builder ───────────────────────────────────────────────────

function buildSmithyRequest(
  request:     Request,
  url:         URL,
  parsed:      ParsedAuth,
  isPresigned: boolean,
) {
  // Collect only the headers that were included in the signature
  const headers: Record<string, string> = {};

  if (!isPresigned) {
    for (const name of parsed.signedHeaders) {
      const value = request.headers.get(name);
      if (value !== null) {
        headers[name] = value;
      } else {
        // A signed header that's not in the request is always a sig mismatch
        // but we let the Smithy comparison catch it for consistency
        headers[name] = "";
      }
    }
  } else {
    // Presigned: only "host" is typically signed
    headers["host"] = url.host;
  }

  // Build query map, excluding auth params for presigned comparison
  const query: Record<string, string> = {};
  for (const [k, v] of url.searchParams.entries()) {
    if (isPresigned && k === "X-Amz-Signature") continue;
    query[k] = v;
  }

  return {
    method:   request.method.toUpperCase(),
    protocol: "https:",
    hostname: url.hostname,
    port:     url.port ? parseInt(url.port, 10) : undefined,
    path:     url.pathname,
    query,
    headers,
    body:     undefined as undefined,
  };
}
