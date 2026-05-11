/**
 * AES-256-GCM encryption via the Web Crypto API (native in Cloudflare Workers).
 *
 * Key caching: importKey is expensive (~0.5 ms). We cache the CryptoKey object
 * using a module-level WeakMap keyed on the hex string so that within a single
 * isolate lifetime the import is paid at most once per unique key string.
 *
 * Wire format (base64url): IV[12 bytes] ‖ ciphertext ‖ GCM-tag[16 bytes]
 */

import { ProxyError } from "@/errors";

// ─── Key import with module-level cache ──────────────────────────────────────

// Module-level Map persists for the lifetime of the isolate (~seconds to minutes).
// Using a Map<string, Promise<CryptoKey>> prevents redundant concurrent imports.
const KEY_CACHE = new Map<string, Promise<CryptoKey>>();

export async function importMasterKey(hexKey: string): Promise<CryptoKey> {
  const cached = KEY_CACHE.get(hexKey);
  if (cached) return cached;

  const promise = (async () => {
    const trimmed = hexKey.trim();
    if (!/^[0-9a-fA-F]{64}$/.test(trimmed)) {
      throw ProxyError.internal(
        "MASTER_ENC_KEY must be exactly 64 hex characters (32 bytes)"
      );
    }
    const raw = hexToBytes(trimmed);
    return crypto.subtle.importKey(
      "raw",
      raw,
      { name: "AES-GCM", length: 256 },
      false, // non-extractable — key never leaves the sandbox
      ["encrypt", "decrypt"],
    );
  })();

  KEY_CACHE.set(hexKey, promise);

  // On failure, remove the rejected promise so the next call retries
  promise.catch(() => KEY_CACHE.delete(hexKey));

  return promise;
}

// ─── Encrypt ─────────────────────────────────────────────────────────────────

/**
 * Encrypt a plaintext string with AES-256-GCM.
 * Returns base64url(IV[12] ‖ ciphertext ‖ GCM-tag[16]).
 */
export async function encrypt(key: CryptoKey, plaintext: string): Promise<string> {
  const iv      = crypto.getRandomValues(new Uint8Array(12));
  const encoded = new TextEncoder().encode(plaintext);

  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv, tagLength: 128 },
    key,
    encoded,
  );

  // Combine: IV (12) ‖ ciphertext+tag
  const blob = new Uint8Array(12 + ciphertext.byteLength);
  blob.set(iv, 0);
  blob.set(new Uint8Array(ciphertext), 12);
  return bytesToBase64Url(blob);
}

// ─── Decrypt ─────────────────────────────────────────────────────────────────

/**
 * Decrypt a value produced by `encrypt`.
 * Throws `ProxyError.internal` if the GCM tag fails (tamper detection).
 */
export async function decrypt(key: CryptoKey, encoded: string): Promise<string> {
  let blob: Uint8Array;
  try {
    blob = base64UrlToBytes(encoded);
  } catch {
    throw ProxyError.internal("Encrypted field has invalid base64url encoding");
  }

  // Minimum: 12 (IV) + 1 (at least 1 byte plaintext) + 16 (tag) = 29
  if (blob.length < 29) {
    throw ProxyError.internal("Encrypted field is too short to be valid ciphertext");
  }

  const iv         = blob.slice(0, 12);
  const ciphertext = blob.slice(12);

  try {
    const plaintext = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv, tagLength: 128 },
      key,
      ciphertext,
    );
    return new TextDecoder().decode(plaintext);
  } catch {
    // Do NOT expose the underlying error — it could leak timing/key info
    throw ProxyError.internal("Credential decryption failed (GCM tag mismatch)");
  }
}

// ─── Constant-time comparison ─────────────────────────────────────────────────

/**
 * Compare two strings in constant time to prevent timing attacks on
 * SigV4 signature verification.
 *
 * Runs in O(n) time regardless of whether strings differ at position 0 or n-1.
 * Both strings must be the same length for a secure comparison; if they differ
 * in length we return false immediately (length IS public information in S3
 * signatures since the client sends it in the Authorization header).
 */
export function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  const ab  = new TextEncoder().encode(a);
  const bb  = new TextEncoder().encode(b);
  let diff  = 0;
  for (let i = 0; i < ab.length; i++) {
    // The bitwise OR accumulates any difference — never short-circuits
    diff |= (ab[i]! ^ bb[i]!);
  }
  return diff === 0;
}

// ─── Encoding helpers ─────────────────────────────────────────────────────────

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    out[i >>> 1] = parseInt(hex.slice(i, i + 2), 16);
  }
  return out;
}

export function bytesToBase64Url(bytes: Uint8Array): string {
  // btoa needs a binary string; convert via charCodeAt
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]!);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function base64UrlToBytes(b64url: string): Uint8Array {
  // Re-add padding stripped during encoding
  const b64     = b64url.replace(/-/g, "+").replace(/_/g, "/");
  const padded  = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
  const binary  = atob(padded);
  const out     = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}
