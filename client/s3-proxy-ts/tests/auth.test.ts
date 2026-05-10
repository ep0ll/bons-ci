/**
 * Auth layer tests — covers parseAuthHeader and the verifier.
 * The SigV4 computation itself is tested by @smithy/signature-v4's own test
 * suite; we test our parsing and integration layer here.
 */
import { describe, it, expect } from "vitest";
import { parseAuthHeader } from "../src/auth/verifier";

describe("parseAuthHeader", () => {
  const VALID_HEADER =
    "AWS4-HMAC-SHA256 " +
    "Credential=AKIAIOSFODNN7EXAMPLE/20240115/us-east-1/s3/aws4_request, " +
    "SignedHeaders=host;x-amz-date;x-amz-content-sha256, " +
    "Signature=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890";

  it("parses a valid Authorization header", () => {
    const parsed = parseAuthHeader(VALID_HEADER);
    expect(parsed.accessKeyId).toBe("AKIAIOSFODNN7EXAMPLE");
    expect(parsed.date).toBe("20240115");
    expect(parsed.region).toBe("us-east-1");
    expect(parsed.service).toBe("s3");
    expect(parsed.signedHeaders).toEqual([
      "host",
      "x-amz-date",
      "x-amz-content-sha256",
    ]);
    expect(parsed.signature).toBe(
      "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
    );
  });

  it("throws on non-AWS4-HMAC-SHA256 auth scheme", () => {
    expect(() => parseAuthHeader("Basic dXNlcjpwYXNz")).toThrow();
  });

  it("throws when Credential field is missing", () => {
    expect(() =>
      parseAuthHeader("AWS4-HMAC-SHA256 SignedHeaders=host, Signature=abc")
    ).toThrow();
  });

  it("throws when Credential has wrong format", () => {
    expect(() =>
      parseAuthHeader(
        "AWS4-HMAC-SHA256 Credential=AKID/20240115/us-east-1/s3/WRONG, " +
          "SignedHeaders=host, Signature=abc"
      )
    ).toThrow(/invalid Credential/i);
  });

  it("throws when signature field is missing", () => {
    expect(() =>
      parseAuthHeader(
        "AWS4-HMAC-SHA256 Credential=AKID/20240115/us-east-1/s3/aws4_request, " +
          "SignedHeaders=host"
      )
    ).toThrow();
  });

  it("handles multiple signed headers in order", () => {
    const header =
      "AWS4-HMAC-SHA256 " +
      "Credential=AKID/20240115/us-east-1/s3/aws4_request, " +
      "SignedHeaders=content-type;host;x-amz-date, " +
      "Signature=abc123";
    const parsed = parseAuthHeader(header);
    expect(parsed.signedHeaders).toEqual([
      "content-type",
      "host",
      "x-amz-date",
    ]);
  });
});
