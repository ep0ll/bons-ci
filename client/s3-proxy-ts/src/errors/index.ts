/**
 * S3-compatible error taxonomy.
 *
 * Every ProxyError carries:
 *   - A typed `kind` for pattern-matching inside the proxy
 *   - The exact AWS S3 error `code` string (what the client SDK expects)
 *   - The correct HTTP `status` code
 *   - A human-readable `detail` message (XML-escaped in `toResponse`)
 *   - A `requestId` for log correlation
 *
 * `toResponse()` produces the EXACT XML format the AWS SDK parses for
 * error detection. Any deviation breaks SDK retry logic.
 */

// ─── Error metadata map ───────────────────────────────────────────────────────

const ERROR_META = {
  // 400 Bad Request
  MalformedXML:            { s3Code: "MalformedXML",              status: 400 },
  InvalidArgument:         { s3Code: "InvalidArgument",           status: 400 },
  InvalidBucketName:       { s3Code: "InvalidBucketName",         status: 400 },
  InvalidPart:             { s3Code: "InvalidPart",               status: 400 },
  InvalidPartOrder:        { s3Code: "InvalidPartOrder",          status: 400 },
  TooManyParts:            { s3Code: "TooManyParts",              status: 400 },
  EntityTooLarge:          { s3Code: "EntityTooLarge",            status: 400 },
  EntityTooSmall:          { s3Code: "EntityTooSmall",            status: 400 },
  KeyTooLongError:         { s3Code: "KeyTooLongError",           status: 400 },
  MaxMessageLengthExceeded:{ s3Code: "MaxMessageLengthExceeded",  status: 400 },
  // 403 Forbidden
  NoAuth:                  { s3Code: "InvalidRequest",            status: 403 },
  MalformedAuth:           { s3Code: "InvalidRequest",            status: 403 },
  InvalidAccessKeyId:      { s3Code: "InvalidAccessKeyId",        status: 403 },
  SignatureDoesNotMatch:   { s3Code: "SignatureDoesNotMatch",      status: 403 },
  RequestExpired:          { s3Code: "RequestExpired",            status: 403 },
  AccessDenied:            { s3Code: "AccessDenied",              status: 403 },
  // 404 Not Found
  NoSuchBucket:            { s3Code: "NoSuchBucket",              status: 404 },
  NoSuchKey:               { s3Code: "NoSuchKey",                 status: 404 },
  NoSuchUpload:            { s3Code: "NoSuchUpload",              status: 404 },
  // 405 Method Not Allowed
  MethodNotAllowed:        { s3Code: "MethodNotAllowed",          status: 405 },
  // 409 Conflict
  BucketAlreadyExists:     { s3Code: "BucketAlreadyExists",       status: 409 },
  BucketAlreadyOwnedByYou: { s3Code: "BucketAlreadyOwnedByYou",  status: 409 },
  BucketNotEmpty:          { s3Code: "BucketNotEmpty",            status: 409 },
  // 412 Precondition Failed
  PreconditionFailed:      { s3Code: "PreconditionFailed",        status: 412 },
  // 416 Range Not Satisfiable
  InvalidRange:            { s3Code: "InvalidRange",              status: 416 },
  // 500 Internal Server Error
  InternalError:           { s3Code: "InternalError",             status: 500 },
  // 501 Not Implemented
  NotImplemented:          { s3Code: "NotImplemented",            status: 501 },
  // 503 Service Unavailable
  SlowDown:                { s3Code: "SlowDown",                  status: 503 },
  ServiceUnavailable:      { s3Code: "ServiceUnavailable",        status: 503 },
} as const satisfies Record<string, { s3Code: string; status: number }>;

export type ErrorKind = keyof typeof ERROR_META;

// ─── ProxyError ───────────────────────────────────────────────────────────────

export class ProxyError extends Error {
  readonly kind:      ErrorKind;
  readonly detail:    string;
  readonly s3Code:    string;
  readonly status:    number;
  readonly requestId: string;

  constructor(kind: ErrorKind, detail = "") {
    super(`${kind}${detail ? ": " + detail : ""}`);
    this.name      = "ProxyError";
    this.kind      = kind;
    this.detail    = detail;
    const meta     = ERROR_META[kind];
    this.s3Code    = meta.s3Code;
    this.status    = meta.status;
    this.requestId = generateRequestId();
    // Maintain proper stack trace in V8
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, ProxyError);
    }
  }

  // ── Convenience factories (all S3 documented error codes) ─────────────────

  static noAuth()                           { return new ProxyError("NoAuth"); }
  static malformedAuth(d: string)           { return new ProxyError("MalformedAuth", d); }
  static invalidKeyId()                     { return new ProxyError("InvalidAccessKeyId"); }
  static signatureMismatch()                { return new ProxyError("SignatureDoesNotMatch"); }
  static requestExpired()                   { return new ProxyError("RequestExpired"); }
  static accessDenied(d = "")              { return new ProxyError("AccessDenied", d); }
  static noSuchBucket(b: string)            { return new ProxyError("NoSuchBucket", b); }
  static noSuchKey(k: string)               { return new ProxyError("NoSuchKey", k); }
  static noSuchUpload(u: string)            { return new ProxyError("NoSuchUpload", u); }
  static bucketAlreadyExists(b: string)     { return new ProxyError("BucketAlreadyExists", b); }
  static bucketAlreadyOwnedByYou(b: string) { return new ProxyError("BucketAlreadyOwnedByYou", b); }
  static bucketNotEmpty(b: string)          { return new ProxyError("BucketNotEmpty", b); }
  static malformedXML(d: string)            { return new ProxyError("MalformedXML", d); }
  static invalidArgument(d: string)         { return new ProxyError("InvalidArgument", d); }
  static invalidBucketName(d: string)       { return new ProxyError("InvalidBucketName", d); }
  static invalidPart(d: string)             { return new ProxyError("InvalidPart", d); }
  static invalidPartOrder(d: string)        { return new ProxyError("InvalidPartOrder", d); }
  static tooManyParts(d: string)            { return new ProxyError("TooManyParts", d); }
  static entityTooLarge(d: string)          { return new ProxyError("EntityTooLarge", d); }
  static keyTooLong(d: string)              { return new ProxyError("KeyTooLongError", d); }
  static methodNotAllowed()                 { return new ProxyError("MethodNotAllowed"); }
  static notImplemented(d: string)          { return new ProxyError("NotImplemented", d); }
  static internal(d: string)                { return new ProxyError("InternalError", d); }
  static slowDown(d = "")                  { return new ProxyError("SlowDown", d); }

  /** Wrap any unknown thrown value into a ProxyError, preserving context. */
  static from(err: unknown, context: string): ProxyError {
    if (err instanceof ProxyError) return err;
    const msg = err instanceof Error ? err.message : String(err);
    console.error(`[ProxyError.from] ${context}:`, err);
    return ProxyError.internal(`${context}: ${msg}`);
  }

  /**
   * Convert this error to an S3-compatible XML `Response`.
   *
   * The XML format is EXACTLY what the AWS SDK parser expects for error
   * detection and retry classification. Do not change the field order.
   */
  toResponse(): Response {
    const body = xmlErrorBody(this.s3Code, this.detail || this.kind, this.requestId);
    return new Response(body, {
      status: this.status,
      headers: {
        "Content-Type":    "application/xml",
        "x-amz-request-id": this.requestId,
        // Prevent any proxy caching of error responses
        "Cache-Control":   "no-store",
      },
    });
  }
}

// ─── XML error body ───────────────────────────────────────────────────────────

function xmlErrorBody(code: string, message: string, requestId: string): string {
  return [
    `<?xml version="1.0" encoding="UTF-8"?>`,
    `<Error>`,
    `  <Code>${xmlEscape(code)}</Code>`,
    `  <Message>${xmlEscape(message)}</Message>`,
    `  <RequestId>${xmlEscape(requestId)}</RequestId>`,
    `  <HostId>${xmlEscape(requestId)}</HostId>`,
    `</Error>`,
  ].join("\n");
}

// ─── XML escaping ─────────────────────────────────────────────────────────────

/** Escape characters that are special in XML content. */
export function xmlEscape(s: string): string {
  // Replace in a single pass using a lookup table — faster than chained replace
  let out = "";
  for (let i = 0; i < s.length; i++) {
    const c = s[i]!;
    switch (c) {
      case "&":  out += "&amp;";  break;
      case "<":  out += "&lt;";   break;
      case ">":  out += "&gt;";   break;
      case '"':  out += "&quot;"; break;
      case "'":  out += "&apos;"; break;
      default:   out += c;
    }
  }
  return out;
}

// ─── Error boundary ───────────────────────────────────────────────────────────

/**
 * Wrap an async handler in a try/catch that always returns an S3-compatible
 * `Response`, even on unexpected throws.
 *
 * Uses a type-safe overload so the return type is always `Promise<Response>`.
 */
export async function withErrorBoundary(fn: () => Promise<Response>): Promise<Response> {
  try {
    return await fn();
  } catch (err) {
    if (err instanceof ProxyError) return err.toResponse();
    // Unexpected error — log the full stack, return generic 500
    console.error("Unhandled exception:", err);
    return ProxyError.from(err, "request handler").toResponse();
  }
}

// ─── Helper ──────────────────────────────────────────────────────────────────

function generateRequestId(): string {
  // 32 hex chars matching the format AWS uses
  return Array.from(crypto.getRandomValues(new Uint8Array(16)))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
