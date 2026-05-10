/**
 * Crypto layer tests.
 *
 * Uses the Web Crypto API via globalThis.crypto which Vitest provides via
 * the `environment: "edge-runtime"` config in vitest.config.ts.
 */
import { describe, it, expect } from "vitest";
import {
  importMasterKey,
  encrypt,
  decrypt,
  constantTimeEqual,
} from "../src/crypto";

// ─── importMasterKey ──────────────────────────────────────────────────────────

describe("importMasterKey", () => {
  it("imports a valid 64-hex-char key", async () => {
    const key = await importMasterKey("a".repeat(64));
    expect(key).toBeInstanceOf(CryptoKey);
    expect(key.type).toBe("secret");
    expect(key.algorithm).toMatchObject({ name: "AES-GCM" });
    expect(key.extractable).toBe(false);
  });

  it("throws on key that is too short", async () => {
    await expect(importMasterKey("a".repeat(62))).rejects.toThrow(/32 bytes/);
  });

  it("throws on key that is too long", async () => {
    await expect(importMasterKey("a".repeat(66))).rejects.toThrow(/32 bytes/);
  });

  it("throws on non-hex input", async () => {
    await expect(importMasterKey("z".repeat(64))).rejects.toThrow();
  });
});

// ─── encrypt / decrypt round-trip ─────────────────────────────────────────────

describe("encrypt and decrypt", () => {
  async function makeKey(): Promise<CryptoKey> {
    return importMasterKey("0".repeat(64));
  }

  it("round-trips a simple string", async () => {
    const key = await makeKey();
    const plaintext = "hello, world";
    const enc = await encrypt(key, plaintext);
    const dec = await decrypt(key, enc);
    expect(dec).toBe(plaintext);
  });

  it("round-trips an empty string", async () => {
    const key = await makeKey();
    const enc = await encrypt(key, "");
    const dec = await decrypt(key, enc);
    expect(dec).toBe("");
  });

  it("round-trips a long secret key string", async () => {
    const key = await makeKey();
    const secret =
      "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY+extra-padding-here";
    const enc = await encrypt(key, secret);
    const dec = await decrypt(key, enc);
    expect(dec).toBe(secret);
  });

  it("round-trips unicode characters", async () => {
    const key = await makeKey();
    const text = "日本語テスト 🔐 émoji";
    const enc = await encrypt(key, text);
    const dec = await decrypt(key, enc);
    expect(dec).toBe(text);
  });

  it("produces different ciphertext on each call (random IV)", async () => {
    const key = await makeKey();
    const plain = "same plaintext";
    const enc1 = await encrypt(key, plain);
    const enc2 = await encrypt(key, plain);
    // Different random IVs → different ciphertexts
    expect(enc1).not.toBe(enc2);
    // But both decrypt correctly
    expect(await decrypt(key, enc1)).toBe(plain);
    expect(await decrypt(key, enc2)).toBe(plain);
  });

  it("output is base64url (no +, /, or = chars)", async () => {
    const key = await makeKey();
    const enc = await encrypt(key, "test");
    expect(enc).not.toContain("+");
    expect(enc).not.toContain("/");
    expect(enc).not.toContain("=");
  });

  it("throws when decrypting with wrong key", async () => {
    const key1 = await importMasterKey("a".repeat(64));
    const key2 = await importMasterKey("b".repeat(64));
    const enc = await encrypt(key1, "secret");
    await expect(decrypt(key2, enc)).rejects.toThrow();
  });

  it("throws on truncated ciphertext", async () => {
    const key = await makeKey();
    const enc = await encrypt(key, "test");
    const corrupt = enc.slice(0, 10); // way too short (< 12+16 bytes)
    await expect(decrypt(key, corrupt)).rejects.toThrow(/too short/i);
  });

  it("throws when ciphertext is tampered (GCM tag failure)", async () => {
    const key = await makeKey();
    const enc = await encrypt(key, "sensitive data");
    // Flip the last character to corrupt the GCM tag
    const chars = enc.split("");
    chars[chars.length - 1] = chars[chars.length - 1] === "A" ? "B" : "A";
    const tampered = chars.join("");
    await expect(decrypt(key, tampered)).rejects.toThrow();
  });
});

// ─── constantTimeEqual ────────────────────────────────────────────────────────

describe("constantTimeEqual", () => {
  it("returns true for equal strings", () => {
    expect(constantTimeEqual("abc123def456", "abc123def456")).toBe(true);
  });

  it("returns false for strings of different lengths", () => {
    expect(constantTimeEqual("abc", "abcd")).toBe(false);
    expect(constantTimeEqual("abcd", "abc")).toBe(false);
  });

  it("returns false for equal-length strings with different content", () => {
    expect(constantTimeEqual("abc123", "abc124")).toBe(false);
    expect(constantTimeEqual("abc123", "ABC123")).toBe(false);
  });

  it("returns true for empty strings", () => {
    expect(constantTimeEqual("", "")).toBe(true);
  });

  it("returns false when one string is empty", () => {
    expect(constantTimeEqual("", "a")).toBe(false);
    expect(constantTimeEqual("a", "")).toBe(false);
  });

  it("handles long hex signatures correctly", () => {
    const sig = "a".repeat(64);
    expect(constantTimeEqual(sig, sig)).toBe(true);
    expect(constantTimeEqual(sig, "b".repeat(64))).toBe(false);
    // One char different at the start
    expect(constantTimeEqual("b" + "a".repeat(63), "a".repeat(64))).toBe(false);
    // One char different at the end
    expect(constantTimeEqual("a".repeat(63) + "b", "a".repeat(64))).toBe(false);
  });
});
