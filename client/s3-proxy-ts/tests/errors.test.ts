import { describe, it, expect } from "vitest";
import { ProxyError, xmlEscape, withErrorBoundary } from "../src/errors";

// ─── ProxyError construction ──────────────────────────────────────────────────

describe("ProxyError", () => {
  it("stores kind, s3Code, status, and detail", () => {
    const e = new ProxyError("NoSuchBucket", "my-bucket");
    expect(e.kind).toBe("NoSuchBucket");
    expect(e.s3Code).toBe("NoSuchBucket");
    expect(e.status).toBe(404);
    expect(e.detail).toBe("my-bucket");
    expect(e.name).toBe("ProxyError");
  });

  it("instanceof Error", () => {
    expect(new ProxyError("InternalError") instanceof Error).toBe(true);
    expect(new ProxyError("InternalError") instanceof ProxyError).toBe(true);
  });

  it("generates a unique requestId per instance", () => {
    const a = new ProxyError("AccessDenied");
    const b = new ProxyError("AccessDenied");
    expect(a.requestId).not.toBe(b.requestId);
    expect(a.requestId).toMatch(/^[0-9a-f]{32}$/);
  });
});

// ─── Factory methods ──────────────────────────────────────────────────────────

describe("ProxyError factories", () => {
  const cases: [string, ProxyError, number][] = [
    ["noAuth",                ProxyError.noAuth(),                   403],
    ["signatureMismatch",     ProxyError.signatureMismatch(),        403],
    ["requestExpired",        ProxyError.requestExpired(),           403],
    ["invalidKeyId",          ProxyError.invalidKeyId(),             403],
    ["accessDenied",          ProxyError.accessDenied(),             403],
    ["noSuchBucket",          ProxyError.noSuchBucket("b"),         404],
    ["noSuchKey",             ProxyError.noSuchKey("k"),            404],
    ["noSuchUpload",          ProxyError.noSuchUpload("u"),         404],
    ["bucketAlreadyExists",   ProxyError.bucketAlreadyExists("b"),  409],
    ["bucketAlreadyOwnedByYou", ProxyError.bucketAlreadyOwnedByYou("b"), 409],
    ["bucketNotEmpty",        ProxyError.bucketNotEmpty("b"),        409],
    ["malformedXML",          ProxyError.malformedXML("bad xml"),   400],
    ["invalidArgument",       ProxyError.invalidArgument("msg"),    400],
    ["invalidBucketName",     ProxyError.invalidBucketName("n"),    400],
    ["invalidPartOrder",      ProxyError.invalidPartOrder("d"),     400],
    ["notImplemented",        ProxyError.notImplemented("op"),      501],
    ["internal",              ProxyError.internal("err"),           500],
    ["methodNotAllowed",      ProxyError.methodNotAllowed(),        405],
    ["slowDown",              ProxyError.slowDown(),                503],
  ];

  it.each(cases)("%s returns correct HTTP status %i", (_, err, status) => {
    expect(err.status).toBe(status);
    expect(err instanceof ProxyError).toBe(true);
  });
});

// ─── ProxyError.from ──────────────────────────────────────────────────────────

describe("ProxyError.from", () => {
  it("passes through a ProxyError unchanged", () => {
    const original = ProxyError.accessDenied();
    expect(ProxyError.from(original, "ctx")).toBe(original);
  });

  it("wraps a regular Error", () => {
    const err = new Error("connection refused");
    const pe  = ProxyError.from(err, "MyService.connect");
    expect(pe.kind).toBe("InternalError");
    expect(pe.detail).toContain("MyService.connect");
    expect(pe.detail).toContain("connection refused");
  });

  it("wraps a string", () => {
    const pe = ProxyError.from("raw error", "context");
    expect(pe.kind).toBe("InternalError");
    expect(pe.detail).toContain("raw error");
  });

  it("wraps null/undefined gracefully", () => {
    expect(() => ProxyError.from(null, "ctx")).not.toThrow();
    expect(() => ProxyError.from(undefined, "ctx")).not.toThrow();
  });
});

// ─── toResponse ───────────────────────────────────────────────────────────────

describe("ProxyError.toResponse", () => {
  it("returns correct status code", () => {
    expect(ProxyError.noSuchBucket("b").toResponse().status).toBe(404);
    expect(ProxyError.signatureMismatch().toResponse().status).toBe(403);
    expect(ProxyError.internal("x").toResponse().status).toBe(500);
  });

  it("Content-Type is application/xml", () => {
    const resp = ProxyError.noAuth().toResponse();
    expect(resp.headers.get("Content-Type")).toBe("application/xml");
  });

  it("x-amz-request-id header is a 32-char hex string", () => {
    const resp = ProxyError.noAuth().toResponse();
    const id   = resp.headers.get("x-amz-request-id");
    expect(id).toMatch(/^[0-9a-f]{32}$/);
  });

  it("Cache-Control is no-store", () => {
    const resp = ProxyError.noAuth().toResponse();
    expect(resp.headers.get("Cache-Control")).toBe("no-store");
  });

  it("body has XML declaration", async () => {
    const body = await ProxyError.noAuth().toResponse().text();
    expect(body).toContain('<?xml version="1.0" encoding="UTF-8"?>');
  });

  it("body has <Code> element matching s3Code", async () => {
    const body = await ProxyError.signatureMismatch().toResponse().text();
    expect(body).toContain("<Code>SignatureDoesNotMatch</Code>");
  });

  it("body has <Message> with detail", async () => {
    const body = await ProxyError.invalidArgument("partNumber must be 1-10000").toResponse().text();
    expect(body).toContain("<Message>partNumber must be 1-10000</Message>");
  });

  it("body has <RequestId> matching the header", async () => {
    const err  = ProxyError.internal("test");
    const resp = err.toResponse();
    const body = await resp.text();
    const headerId = resp.headers.get("x-amz-request-id");
    expect(body).toContain(`<RequestId>${headerId}</RequestId>`);
  });

  it("body has <HostId> element", async () => {
    const body = await ProxyError.noSuchKey("k").toResponse().text();
    expect(body).toContain("<HostId>");
  });

  it("XML-escapes detail with special characters", async () => {
    const body = await ProxyError.internal('Error: "quotes" & <tags>').toResponse().text();
    expect(body).toContain("&quot;quotes&quot;");
    expect(body).toContain("&amp;");
    expect(body).toContain("&lt;tags&gt;");
    // Raw characters must not appear in the message
    const msgMatch = body.match(/<Message>(.*?)<\/Message>/s);
    expect(msgMatch?.[1]).not.toContain("<");
    expect(msgMatch?.[1]).not.toContain(">");
    expect(msgMatch?.[1]).not.toMatch(/&(?!(?:amp|lt|gt|quot|apos);)/);
  });

  it("all distinct error kinds have unique s3Code values", () => {
    const errors = [
      ProxyError.noAuth(), ProxyError.invalidKeyId(), ProxyError.signatureMismatch(),
      ProxyError.noSuchBucket("b"), ProxyError.noSuchKey("k"), ProxyError.noSuchUpload("u"),
      ProxyError.bucketAlreadyExists("b"), ProxyError.bucketNotEmpty("b"),
      ProxyError.notImplemented("op"), ProxyError.internal("x"),
    ];
    const codes  = errors.map((e) => e.s3Code);
    const unique = new Set(codes);
    expect(unique.size).toBe(codes.length);
  });
});

// ─── xmlEscape ────────────────────────────────────────────────────────────────

describe("xmlEscape", () => {
  it("escapes all five XML special characters", () => {
    expect(xmlEscape("& < > \" '")).toBe("&amp; &lt; &gt; &quot; &apos;");
  });

  it("leaves normal strings unchanged", () => {
    expect(xmlEscape("hello world 123")).toBe("hello world 123");
  });

  it("handles empty string", () => {
    expect(xmlEscape("")).toBe("");
  });

  it("handles strings with no special chars", () => {
    expect(xmlEscape("abc-DEF_123")).toBe("abc-DEF_123");
  });

  it("escapes multiple occurrences", () => {
    expect(xmlEscape("a&b&c")).toBe("a&amp;b&amp;c");
    expect(xmlEscape("<<>>")).toBe("&lt;&lt;&gt;&gt;");
  });

  it("handles unicode characters (no escaping needed)", () => {
    const s = "日本語 emoji 🔐";
    expect(xmlEscape(s)).toBe(s);
  });
});

// ─── withErrorBoundary ────────────────────────────────────────────────────────

describe("withErrorBoundary", () => {
  it("passes through a successful Response", async () => {
    const resp = await withErrorBoundary(async () => new Response("ok", { status: 200 }));
    expect(resp.status).toBe(200);
    expect(await resp.text()).toBe("ok");
  });

  it("converts a ProxyError to an XML Response", async () => {
    const resp = await withErrorBoundary(async () => {
      throw ProxyError.noSuchBucket("photos");
    });
    expect(resp.status).toBe(404);
    expect(resp.headers.get("Content-Type")).toBe("application/xml");
    const body = await resp.text();
    expect(body).toContain("<Code>NoSuchBucket</Code>");
  });

  it("converts an unknown Error to a 500 XML Response", async () => {
    const resp = await withErrorBoundary(async () => {
      throw new Error("unexpected failure");
    });
    expect(resp.status).toBe(500);
    const body = await resp.text();
    expect(body).toContain("<Code>InternalError</Code>");
  });

  it("converts a thrown string to a 500 XML Response", async () => {
    const resp = await withErrorBoundary(async () => { throw "raw string error"; });
    expect(resp.status).toBe(500);
  });

  it("does not swallow the error details for 500s", async () => {
    const resp = await withErrorBoundary(async () => {
      throw new Error("database connection failed");
    });
    const body = await resp.text();
    expect(body).toContain("database connection failed");
  });
});
