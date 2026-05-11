import { describe, it, expect, beforeAll } from "vitest";
import { importMasterKey, encrypt, decrypt, constantTimeEqual } from "../src/crypto";

// ─── importMasterKey ──────────────────────────────────────────────────────────

describe("importMasterKey", () => {
  it("imports a valid 64-hex-char key", async () => {
    const key = await importMasterKey("a".repeat(64));
    expect(key).toBeInstanceOf(CryptoKey);
    expect(key.type).toBe("secret");
    expect(key.extractable).toBe(false);
    expect((key.algorithm as { name: string }).name).toBe("AES-GCM");
  });

  it("caches: returns same promise for same hex string", async () => {
    const hex = "b".repeat(64);
    const p1  = importMasterKey(hex);
    const p2  = importMasterKey(hex);
    expect(p1).toBe(p2); // exact same Promise object
  });

  it("trims whitespace from hex string", async () => {
    const key = await importMasterKey("  " + "c".repeat(64) + "\n");
    expect(key).toBeInstanceOf(CryptoKey);
  });

  it("throws on key that is too short", async () => {
    await expect(importMasterKey("a".repeat(62))).rejects.toThrow(/64 hex characters/);
  });

  it("throws on key that is too long", async () => {
    await expect(importMasterKey("a".repeat(66))).rejects.toThrow(/64 hex characters/);
  });

  it("throws on non-hex characters", async () => {
    await expect(importMasterKey("z".repeat(64))).rejects.toThrow(/64 hex characters/);
  });

  it("throws on empty string", async () => {
    await expect(importMasterKey("")).rejects.toThrow();
  });

  it("removes failed import from cache so retry works", async () => {
    // First call with invalid key fails
    await expect(importMasterKey("g".repeat(64))).rejects.toThrow();
    // Second call with valid key should succeed (cache was cleaned up)
    const key = await importMasterKey("a".repeat(64));
    expect(key).toBeInstanceOf(CryptoKey);
  });
});

// ─── encrypt / decrypt round-trip ─────────────────────────────────────────────

describe("encrypt and decrypt", () => {
  let key: CryptoKey;
  beforeAll(async () => { key = await importMasterKey("0".repeat(64)); });

  it("round-trips a simple string", async () => {
    const enc = await encrypt(key, "hello world");
    const dec = await decrypt(key, enc);
    expect(dec).toBe("hello world");
  });

  it("round-trips an empty string", async () => {
    const enc = await encrypt(key, "");
    const dec = await decrypt(key, enc);
    expect(dec).toBe("");
  });

  it("round-trips a long secret key", async () => {
    const secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY+suffix/extra=padding";
    const enc    = await encrypt(key, secret);
    const dec    = await decrypt(key, enc);
    expect(dec).toBe(secret);
  });

  it("round-trips unicode content", async () => {
    const text = "日本語テスト 🔐 emoji café naïve résumé";
    const enc  = await encrypt(key, text);
    const dec  = await decrypt(key, enc);
    expect(dec).toBe(text);
  });

  it("round-trips a JSON string (credential-like)", async () => {
    const json = JSON.stringify({ kind: "b2", endpoint: "s3.us-west-004.backblazeb2.com", secret: "abc123" });
    const enc  = await encrypt(key, json);
    const dec  = await decrypt(key, enc);
    expect(JSON.parse(dec)).toEqual(JSON.parse(json));
  });

  it("produces different ciphertext on each call (random IV)", async () => {
    const plain = "same plaintext";
    const enc1  = await encrypt(key, plain);
    const enc2  = await encrypt(key, plain);
    expect(enc1).not.toBe(enc2);
    // Both still decrypt correctly
    expect(await decrypt(key, enc1)).toBe(plain);
    expect(await decrypt(key, enc2)).toBe(plain);
  });

  it("output is base64url — no +, /, or = characters", async () => {
    for (let i = 0; i < 10; i++) {
      const enc = await encrypt(key, `test-${i}`);
      expect(enc).not.toContain("+");
      expect(enc).not.toContain("/");
      expect(enc).not.toContain("=");
    }
  });

  it("throws when decrypting with wrong key", async () => {
    const key2 = await importMasterKey("f".repeat(64));
    const enc  = await encrypt(key, "secret");
    await expect(decrypt(key2, enc)).rejects.toThrow(/decryption failed|GCM/i);
  });

  it("throws on ciphertext that is too short", async () => {
    // Less than 12 (IV) + 1 + 16 (tag) = 29 bytes → base64url < 39 chars
    await expect(decrypt(key, "dG9vc2hvcnQ")).rejects.toThrow(/too short/i);
  });

  it("throws when ciphertext is tampered (GCM tag mismatch)", async () => {
    const enc   = await encrypt(key, "sensitive");
    // Flip the very last base64url character
    const chars = enc.split("");
    const last  = chars[chars.length - 1]!;
    chars[chars.length - 1] = last === "A" ? "B" : "A";
    await expect(decrypt(key, chars.join(""))).rejects.toThrow();
  });

  it("throws on invalid base64url encoding", async () => {
    await expect(decrypt(key, "not!valid@base64#url")).rejects.toThrow();
  });
});

// ─── constantTimeEqual ────────────────────────────────────────────────────────

describe("constantTimeEqual", () => {
  it("returns true for equal strings", () => {
    expect(constantTimeEqual("abc123def456", "abc123def456")).toBe(true);
  });

  it("returns true for empty strings", () => {
    expect(constantTimeEqual("", "")).toBe(true);
  });

  it("returns false for different lengths", () => {
    expect(constantTimeEqual("abc", "abcd")).toBe(false);
    expect(constantTimeEqual("abcd", "abc")).toBe(false);
    expect(constantTimeEqual("a", "")).toBe(false);
    expect(constantTimeEqual("", "a")).toBe(false);
  });

  it("returns false for equal-length strings with different content", () => {
    expect(constantTimeEqual("abc123", "abc124")).toBe(false);
    expect(constantTimeEqual("abc123", "ABC123")).toBe(false);
    expect(constantTimeEqual("x".repeat(64), "y".repeat(64))).toBe(false);
  });

  it("handles full 64-char hex SigV4 signatures", () => {
    const sig = "a".repeat(64);
    expect(constantTimeEqual(sig, sig)).toBe(true);
    // One char different at position 0
    expect(constantTimeEqual("b" + "a".repeat(63), sig)).toBe(false);
    // One char different at position 63 (last)
    expect(constantTimeEqual("a".repeat(63) + "b", sig)).toBe(false);
    // One char different in the middle
    const mid = sig.slice(0, 32) + "b" + sig.slice(33);
    expect(constantTimeEqual(mid, sig)).toBe(false);
  });

  it("does not short-circuit (same result regardless of diff position)", () => {
    // This is a behavioural test — we cannot directly measure timing in unit tests,
    // but we can verify that all positions produce false, not just position 0
    const base = "0123456789abcdef".repeat(4); // 64 chars
    for (let i = 0; i < 64; i++) {
      const modified = base.slice(0, i) + "X" + base.slice(i + 1);
      expect(constantTimeEqual(base, modified)).toBe(false);
    }
  });
});
