/**
 * S3-compatible error types and XML error serialisation.
 *
 * Every ProxyError maps deterministically to an S3 error code + HTTP status.
 * The `toResponse()` method produces the exact XML body the AWS SDK expects.
 */

// ─── S3 error code → HTTP status mapping ─────────────────────────────────────

export const S3_ERROR_MAP = {
  NoAuth: { code: "InvalidRequest", status: 403 },
  MalformedAuth: { code: "InvalidRequest", status: 403 },
  InvalidAccessKeyId: { code: "InvalidAccessKeyId", status: 403 },
  SignatureMismatch: { code: "SignatureDoesNotMatch", status: 403 },
  RequestExpired: { code: "RequestExpired", status: 403 },
  AccessDenied: { code: "AccessDenied", status: 403 },
  NoSuchBucket: { code: "NoSuchBucket", status: 404 },
  NoSuchKey: { code: "NoSuchKey", status: 404 },
  NoSuchUpload: { code: "NoSuchUpload", status: 404 },
  BucketAlreadyExists: { code: "BucketAlreadyExists", status: 409 },
  BucketNotEmpty: { code: "BucketNotEmpty", status: 409 },
  InvalidPartOrder: { code: "InvalidPartOrder", status: 400 },
  BadRequest: { code: "MalformedXML", status: 400 },
  MethodNotAllowed: { code: "MethodNotAllowed", status: 405 },
  NotImplemented: { code: "NotImplemented", status: 501 },
  InternalError: { code: "InternalError", status: 500 },
} as const satisfies Record<string, { code: string; status: number }>;

export type ErrorKind = keyof typeof S3_ERROR_MAP;

// ─── ProxyError class ─────────────────────────────────────────────────────────

export class ProxyError extends Error {
  readonly kind: ErrorKind;
  readonly detail: string;
  readonly s3Code: string;
  readonly status: number;

  constructor(kind: ErrorKind, detail = "") {
    super(`${kind}: ${detail}`);
    this.kind = kind;
    this.detail = detail;
    const meta = S3_ERROR_MAP[kind];
    this.s3Code = meta.code;
    this.status = meta.status;
  }

  // ── Convenience factories ────────────────────────────────────────────────

  static noAuth() {
    return new ProxyError("NoAuth");
  }
  static malformedAuth(d: string) {
    return new ProxyError("MalformedAuth", d);
  }
  static invalidKeyId() {
    return new ProxyError("InvalidAccessKeyId");
  }
  static signatureMismatch() {
    return new ProxyError("SignatureMismatch");
  }
  static requestExpired() {
    return new ProxyError("RequestExpired");
  }
  static accessDenied() {
    return new ProxyError("AccessDenied");
  }
  static noSuchBucket(b: string) {
    return new ProxyError("NoSuchBucket", b);
  }
  static noSuchKey(k: string) {
    return new ProxyError("NoSuchKey", k);
  }
  static noSuchUpload(u: string) {
    return new ProxyError("NoSuchUpload", u);
  }
  static bucketAlreadyExists(b: string) {
    return new ProxyError("BucketAlreadyExists", b);
  }
  static bucketNotEmpty(b: string) {
    return new ProxyError("BucketNotEmpty", b);
  }
  static invalidPartOrder(d: string) {
    return new ProxyError("InvalidPartOrder", d);
  }
  static badRequest(d: string) {
    return new ProxyError("BadRequest", d);
  }
  static notImplemented(d: string) {
    return new ProxyError("NotImplemented", d);
  }
  static internal(d: string) {
    return new ProxyError("InternalError", d);
  }

  /** Wrap an unknown thrown value as a ProxyError. */
  static from(err: unknown, context: string): ProxyError {
    if (err instanceof ProxyError) return err;
    const msg = err instanceof Error ? err.message : String(err);
    return ProxyError.internal(`${context}: ${msg}`);
  }

  // ── S3 XML error response ─────────────────────────────────────────────────

  toResponse(): Response {
    const requestId = crypto.randomUUID().replace(/-/g, "");
    const body = [
      `<?xml version="1.0" encoding="UTF-8"?>`,
      `<Error>`,
      `  <Code>${xmlEscape(this.s3Code)}</Code>`,
      `  <Message>${xmlEscape(this.detail || this.kind)}</Message>`,
      `  <RequestId>${requestId}</RequestId>`,
      `</Error>`,
    ].join("\n");

    return new Response(body, {
      status: this.status,
      headers: {
        "Content-Type": "application/xml",
        "x-amz-request-id": requestId,
      },
    });
  }
}

// ─── XML escape ───────────────────────────────────────────────────────────────

export function xmlEscape(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&apos;");
}

// ─── Error boundary helper ────────────────────────────────────────────────────

/** Catch any error from `fn` and convert it to a ProxyError Response. */
export async function withErrorBoundary(
  fn: () => Promise<Response>
): Promise<Response> {
  try {
    return await fn();
  } catch (err) {
    if (err instanceof ProxyError) return err.toResponse();
    console.error("Unhandled error:", err);
    return ProxyError.internal(
      err instanceof Error ? err.message : "unknown"
    ).toResponse();
  }
}
