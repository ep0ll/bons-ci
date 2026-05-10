/**
 * AES-256-GCM encryption for backend credentials stored in Cloudflare KV.
 *
 * Uses the **Web Crypto API** which is natively available in Cloudflare Workers
 * — no npm library required.  This is zero-dependency, runs in the Workers
 * sandbox, and is hardware-accelerated where available.
 *
 * Wire format (base64url): [ 12-byte IV ][ ciphertext ][ 16-byte GCM tag ]
 */

// ─── Master key management ────────────────────────────────────────────────────

/**
 * Import a 64-hex-char master key string as a CryptoKey.
 * Call this once per request; Workers caches the import internally.
 */
export async function importMasterKey(hexKey: string): Promise<CryptoKey> {
  const raw = hexToBytes(hexKey.trim());
  if (raw.length !== 32) {
    throw new Error("MASTER_ENC_KEY must be exactly 32 bytes (64 hex chars)");
  }
  return crypto.subtle.importKey(
    "raw",
    raw,
    { name: "AES-GCM", length: 256 },
    false, // not extractable — key stays inside the sandbox
    ["encrypt", "decrypt"]
  );
}

// ─── Encrypt ──────────────────────────────────────────────────────────────────

/**
 * Encrypt `plaintext` string with AES-256-GCM.
 * Returns base64url(IV[12] || ciphertext || tag[16]).
 */
export async function encrypt(
  key: CryptoKey,
  plaintext: string
): Promise<string> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const encoded = new TextEncoder().encode(plaintext);
  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv },
    key,
    encoded
  );

  // Layout: IV(12) || ciphertext+tag
  const blob = new Uint8Array(12 + ciphertext.byteLength);
  blob.set(iv);
  blob.set(new Uint8Array(ciphertext), 12);

  return bytesToBase64Url(blob);
}

// ─── Decrypt ──────────────────────────────────────────────────────────────────

/**
 * Decrypt a value produced by `encrypt`.
 * Throws if the GCM tag fails (tamper detection).
 */
export async function decrypt(
  key: CryptoKey,
  encoded: string
): Promise<string> {
  const blob = base64UrlToBytes(encoded);
  if (blob.length < 12 + 16) throw new Error("Ciphertext too short");

  const iv = blob.slice(0, 12);
  const ciphertext = blob.slice(12);

  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv },
    key,
    ciphertext
  );
  return new TextDecoder().decode(plaintext);
}

// ─── Constant-time comparison ─────────────────────────────────────────────────

/**
 * Compare two strings in constant time to prevent timing attacks.
 * Used for SigV4 signature comparison.
 */
export function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  const ab = new TextEncoder().encode(a);
  const bb = new TextEncoder().encode(b);
  let diff = 0;
  for (let i = 0; i < ab.length; i++) {
    diff |= ab[i]! ^ bb[i]!;
  }
  return diff === 0;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function hexToBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0) throw new Error("Invalid hex string");
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    out[i / 2] = parseInt(hex.slice(i, i + 2), 16);
  }
  return out;
}

function bytesToBase64Url(bytes: Uint8Array): string {
  const b64 = btoa(String.fromCharCode(...bytes));
  return b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

function base64UrlToBytes(b64url: string): Uint8Array {
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/");
  const padded = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}
