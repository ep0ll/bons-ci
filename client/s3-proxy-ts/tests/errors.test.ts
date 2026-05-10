/**
 * Error layer tests — ProxyError XML response format.
 */
import { describe, it, expect } from "vitest";
import { ProxyError } from "../src/errors";

describe("ProxyError", () => {
  it("toResponse returns correct HTTP status for 403 errors", () => {
    const resp = ProxyError.signatureMismatch().toResponse();
    expect(resp.status).toBe(403);
  });

  it("toResponse returns 404 for NoSuchKey", () => {
    const resp = ProxyError.noSuchKey("my-key.txt").toResponse();
    expect(resp.status).toBe(404);
  });

  it("toResponse returns 409 for BucketAlreadyExists", () => {
    const resp = ProxyError.bucketAlreadyExists("photos").toResponse();
    expect(resp.status).toBe(409);
  });

  it("toResponse has Content-Type: application/xml", () => {
    const resp = ProxyError.noSuchBucket("b").toResponse();
    expect(resp.headers.get("Content-Type")).toBe("application/xml");
  });

  it("toResponse body contains S3 error code", async () => {
    const resp = ProxyError.signatureMismatch().toResponse();
    const body = await resp.text();
    expect(body).toContain("<Code>SignatureDoesNotMatch</Code>");
  });

  it("toResponse body contains RequestId", async () => {
    const resp = ProxyError.internal("test").toResponse();
    const body = await resp.text();
    expect(body).toContain("<RequestId>");
    expect(body).toMatch(/<RequestId>[a-f0-9]{32}<\/RequestId>/);
  });

  it("toResponse body contains XML declaration", async () => {
    const resp = ProxyError.noAuth().toResponse();
    const body = await resp.text();
    expect(body).toContain('<?xml version="1.0" encoding="UTF-8"?>');
  });

  it("detail appears in the Message field", async () => {
    const resp = ProxyError.badRequest(
      "partNumber must be between 1 and 10000"
    ).toResponse();
    const body = await resp.text();
    expect(body).toContain("<Message>");
    expect(body).toContain("partNumber must be between 1 and 10000");
  });

  it("XML-escapes detail message with special chars", async () => {
    const resp = ProxyError.internal('Error: "quotes" & <tags>').toResponse();
    const body = await resp.text();
    expect(body).toContain("&quot;quotes&quot;");
    expect(body).toContain("&amp;");
    expect(body).toContain("&lt;tags&gt;");
  });

  it("ProxyError.from wraps an Error correctly", () => {
    const err = new Error("connection refused");
    const proxy = ProxyError.from(err, "MyService.connect");
    expect(proxy.kind).toBe("InternalError");
    expect(proxy.detail).toContain("MyService.connect");
    expect(proxy.detail).toContain("connection refused");
  });

  it("ProxyError.from passes through a ProxyError unchanged", () => {
    const original = ProxyError.accessDenied();
    const wrapped = ProxyError.from(original, "context");
    expect(wrapped).toBe(original);
  });

  it("ProxyError.from handles non-Error thrown values", () => {
    const proxy = ProxyError.from("raw string error", "ctx");
    expect(proxy.detail).toContain("raw string error");
  });

  it("each distinct error kind has a unique S3 code", () => {
    const errors = [
      ProxyError.noAuth(),
      ProxyError.invalidKeyId(),
      ProxyError.signatureMismatch(),
      ProxyError.noSuchBucket("b"),
      ProxyError.noSuchKey("k"),
      ProxyError.bucketAlreadyExists("b"),
      ProxyError.bucketNotEmpty("b"),
      ProxyError.accessDenied(),
      ProxyError.notImplemented("op"),
    ];
    const codes = errors.map((e) => e.s3Code);
    const unique = new Set(codes);
    expect(unique.size).toBe(codes.length);
  });
});
