import { describe, it, expect } from "vitest";
import {
  parseAuthHeader,
  parsePresignedParams,
  isValidAmzTimestamp,
  amzTimestampToDate,
  isTimestampFresh,
} from "../src/auth/parser";

// ─── isValidAmzTimestamp ──────────────────────────────────────────────────────

describe("isValidAmzTimestamp", () => {
  it("accepts valid YYYYMMDDTHHmmssZ format", () => {
    expect(isValidAmzTimestamp("20240115T120000Z")).toBe(true);
    expect(isValidAmzTimestamp("20241231T235959Z")).toBe(true);
  });

  it("rejects ISO-8601 with separators", () => {
    expect(isValidAmzTimestamp("2024-01-15T12:00:00Z")).toBe(false);
  });

  it("rejects empty string", () => {
    expect(isValidAmzTimestamp("")).toBe(false);
  });

  it("rejects missing Z suffix", () => {
    expect(isValidAmzTimestamp("20240115T120000")).toBe(false);
  });

  it("rejects wrong length", () => {
    expect(isValidAmzTimestamp("20240115T1200Z")).toBe(false);
    expect(isValidAmzTimestamp("20240115T1200000Z")).toBe(false);
  });
});

// ─── amzTimestampToDate ───────────────────────────────────────────────────────

describe("amzTimestampToDate", () => {
  it("parses a valid AMZ timestamp", () => {
    const d = amzTimestampToDate("20240115T120000Z");
    expect(d.getUTCFullYear()).toBe(2024);
    expect(d.getUTCMonth()).toBe(0); // January = 0
    expect(d.getUTCDate()).toBe(15);
    expect(d.getUTCHours()).toBe(12);
    expect(d.getUTCMinutes()).toBe(0);
    expect(d.getUTCSeconds()).toBe(0);
  });

  it("parses end-of-year timestamp", () => {
    const d = amzTimestampToDate("20241231T235959Z");
    expect(d.getUTCFullYear()).toBe(2024);
    expect(d.getUTCMonth()).toBe(11); // December = 11
    expect(d.getUTCDate()).toBe(31);
  });
});

// ─── isTimestampFresh ─────────────────────────────────────────────────────────

describe("isTimestampFresh", () => {
  it("accepts a timestamp that is exactly now", () => {
    const now = new Date();
    const ts  = `${now.getUTCFullYear()}${String(now.getUTCMonth() + 1).padStart(2, "0")}${String(now.getUTCDate()).padStart(2, "0")}T${String(now.getUTCHours()).padStart(2, "0")}${String(now.getUTCMinutes()).padStart(2, "0")}${String(now.getUTCSeconds()).padStart(2, "0")}Z`;
    expect(isTimestampFresh(ts)).toBe(true);
  });

  it("rejects a timestamp from 16 minutes ago", () => {
    const old = new Date(Date.now() - 16 * 60 * 1000);
    const ts  = `${old.getUTCFullYear()}${String(old.getUTCMonth() + 1).padStart(2, "0")}${String(old.getUTCDate()).padStart(2, "0")}T${String(old.getUTCHours()).padStart(2, "0")}${String(old.getUTCMinutes()).padStart(2, "0")}${String(old.getUTCSeconds()).padStart(2, "0")}Z`;
    expect(isTimestampFresh(ts)).toBe(false);
  });

  it("rejects a timestamp 16 minutes in the future", () => {
    const future = new Date(Date.now() + 16 * 60 * 1000);
    const ts     = `${future.getUTCFullYear()}${String(future.getUTCMonth() + 1).padStart(2, "0")}${String(future.getUTCDate()).padStart(2, "0")}T${String(future.getUTCHours()).padStart(2, "0")}${String(future.getUTCMinutes()).padStart(2, "0")}${String(future.getUTCSeconds()).padStart(2, "0")}Z`;
    expect(isTimestampFresh(ts)).toBe(false);
  });

  it("rejects malformed timestamp", () => {
    expect(isTimestampFresh("not-a-timestamp")).toBe(false);
    expect(isTimestampFresh("")).toBe(false);
  });
});

// ─── parseAuthHeader ──────────────────────────────────────────────────────────

describe("parseAuthHeader", () => {
  const VALID_DATE = (() => {
    const now = new Date();
    return `${now.getUTCFullYear()}${String(now.getUTCMonth() + 1).padStart(2, "0")}${String(now.getUTCDate()).padStart(2, "0")}T${String(now.getUTCHours()).padStart(2, "0")}${String(now.getUTCMinutes()).padStart(2, "0")}${String(now.getUTCSeconds()).padStart(2, "0")}Z`;
  })();
  const DATE_ONLY = VALID_DATE.slice(0, 8);

  function makeHeader(
    akid = "AKIAIOSFODNN7EXAMPLE",
    date = DATE_ONLY,
    region = "us-east-1",
    service = "s3",
    signedHeaders = "host;x-amz-date",
    signature = "a".repeat(64),
  ) {
    return `AWS4-HMAC-SHA256 Credential=${akid}/${date}/${region}/${service}/aws4_request, SignedHeaders=${signedHeaders}, Signature=${signature}`;
  }

  it("parses a valid Authorization header", () => {
    const parsed = parseAuthHeader(makeHeader(), VALID_DATE);
    expect(parsed.accessKeyId).toBe("AKIAIOSFODNN7EXAMPLE");
    expect(parsed.date).toBe(DATE_ONLY);
    expect(parsed.region).toBe("us-east-1");
    expect(parsed.service).toBe("s3");
    expect(parsed.signedHeaders).toEqual(["host", "x-amz-date"]);
    expect(parsed.signature).toBe("a".repeat(64));
    expect(parsed.timestamp).toBe(VALID_DATE);
    expect(parsed.expiresAt).toBeNull();
    expect(parsed.isPresigned).toBe(false);
  });

  it("throws on wrong auth scheme", () => {
    expect(() => parseAuthHeader("Basic dXNlcjpwYXNz", VALID_DATE)).toThrow(/AWS4-HMAC-SHA256/);
  });

  it("throws on missing Credential", () => {
    expect(() =>
      parseAuthHeader(`AWS4-HMAC-SHA256 SignedHeaders=host, Signature=${"a".repeat(64)}`, VALID_DATE)
    ).toThrow(/Credential/);
  });

  it("throws when Credential has wrong terminator", () => {
    expect(() =>
      parseAuthHeader(
        `AWS4-HMAC-SHA256 Credential=AKID/${DATE_ONLY}/us-east-1/s3/WRONG, SignedHeaders=host, Signature=${"a".repeat(64)}`,
        VALID_DATE,
      )
    ).toThrow(/aws4_request/);
  });

  it("throws when date in Credential doesn't match x-amz-date", () => {
    expect(() =>
      parseAuthHeader(makeHeader("AKID", "20200101"), VALID_DATE)
    ).toThrow(/does not match/);
  });

  it("throws when x-amz-date is missing", () => {
    expect(() =>
      parseAuthHeader(makeHeader(), null)
    ).toThrow(/x-amz-date/);
  });

  it("throws when x-amz-date is malformed", () => {
    expect(() =>
      parseAuthHeader(makeHeader(), "not-a-date")
    ).toThrow(/x-amz-date/);
  });

  it("throws when signature is not 64 hex chars", () => {
    expect(() =>
      parseAuthHeader(makeHeader("AKID", DATE_ONLY, "us-east-1", "s3", "host", "short"), VALID_DATE)
    ).toThrow(/64/);
  });

  it("normalises signature to lowercase", () => {
    const parsed = parseAuthHeader(makeHeader("AKID", DATE_ONLY, "us-east-1", "s3", "host", "A".repeat(64)), VALID_DATE);
    expect(parsed.signature).toBe("a".repeat(64));
  });

  it("parses multiple signed headers", () => {
    const parsed = parseAuthHeader(
      makeHeader("AKID", DATE_ONLY, "us-east-1", "s3", "content-type;host;x-amz-content-sha256;x-amz-date"),
      VALID_DATE,
    );
    expect(parsed.signedHeaders).toEqual([
      "content-type", "host", "x-amz-content-sha256", "x-amz-date",
    ]);
  });
});

// ─── parsePresignedParams ─────────────────────────────────────────────────────

describe("parsePresignedParams", () => {
  function makeParams(overrides: Record<string, string> = {}): URLSearchParams {
    const now    = new Date();
    const nowTs  = `${now.getUTCFullYear()}${String(now.getUTCMonth() + 1).padStart(2, "0")}${String(now.getUTCDate()).padStart(2, "0")}T${String(now.getUTCHours()).padStart(2, "0")}${String(now.getUTCMinutes()).padStart(2, "0")}${String(now.getUTCSeconds()).padStart(2, "0")}Z`;
    const dateOnly = nowTs.slice(0, 8);
    return new URLSearchParams({
      "X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
      "X-Amz-Credential":    `AKID/${dateOnly}/us-east-1/s3/aws4_request`,
      "X-Amz-Date":          nowTs,
      "X-Amz-Expires":       "900",
      "X-Amz-SignedHeaders": "host",
      "X-Amz-Signature":     "b".repeat(64),
      ...overrides,
    });
  }

  it("parses valid presigned params", () => {
    const parsed = parsePresignedParams(makeParams());
    expect(parsed.accessKeyId).toBe("AKID");
    expect(parsed.region).toBe("us-east-1");
    expect(parsed.service).toBe("s3");
    expect(parsed.signedHeaders).toEqual(["host"]);
    expect(parsed.isPresigned).toBe(true);
    expect(parsed.expiresAt).toBeInstanceOf(Date);
  });

  it("throws on wrong algorithm", () => {
    expect(() =>
      parsePresignedParams(makeParams({ "X-Amz-Algorithm": "AWS2" }))
    ).toThrow(/algorithm/i);
  });

  it("throws on missing X-Amz-Credential", () => {
    const params = makeParams();
    params.delete("X-Amz-Credential");
    expect(() => parsePresignedParams(params)).toThrow(/X-Amz-Credential/);
  });

  it("throws on expires > 604800", () => {
    expect(() =>
      parsePresignedParams(makeParams({ "X-Amz-Expires": "604801" }))
    ).toThrow(/604800/);
  });

  it("throws on expires = 0", () => {
    expect(() =>
      parsePresignedParams(makeParams({ "X-Amz-Expires": "0" }))
    ).toThrow();
  });

  it("computes expiresAt correctly", () => {
    const parsed = parsePresignedParams(makeParams({ "X-Amz-Expires": "3600" }));
    const diff   = parsed.expiresAt!.getTime() - Date.now();
    // Should be approximately 3600 seconds from now (within ±5 seconds for timing slack)
    expect(diff).toBeGreaterThan((3600 - 5) * 1000);
    expect(diff).toBeLessThan((3600 + 5) * 1000);
  });
});
