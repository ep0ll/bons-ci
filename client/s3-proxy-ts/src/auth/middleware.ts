/**
 * Hono authentication middleware.
 *
 * Intercepts every request, verifies the SigV4 Authorization header,
 * and injects a `TenantContext` into the Hono context for downstream handlers.
 *
 * On failure, returns the appropriate S3-compatible XML error immediately.
 */
import type { Context, Next } from "hono";
import { parseAuthHeader, verifySigV4 } from "./verifier";
import { TenantRegistry } from "@/tenant/registry";
import { decrypt } from "@/crypto";
import { ProxyError, withErrorBoundary } from "@/errors";
import type { TenantContext } from "@/tenant/types";

/** Hono context variable key for the tenant context. */
export const TENANT_CTX_KEY = "tenantCtx" as const;

declare module "hono" {
  interface ContextVariableMap {
    tenantCtx: TenantContext;
    registry: TenantRegistry;
  }
}

/**
 * Hono middleware that:
 * 1. Parses the Authorization header
 * 2. Looks up the tenant in KV
 * 3. Decrypts the tenant secret key
 * 4. Verifies the SigV4 signature using @smithy/signature-v4
 * 5. Injects the TenantContext into hono's context variables
 */
export function s3AuthMiddleware(env: Env) {
  return async (c: Context, next: Next): Promise<Response | void> => {
    return withErrorBoundary(async () => {
      const authHeader = c.req.header("Authorization");
      if (!authHeader) throw ProxyError.noAuth();

      const parsedAuth = parseAuthHeader(authHeader);
      const registry = await TenantRegistry.create(env);
      const record = await registry.findByAccessKeyId(parsedAuth.accessKeyId);

      // Decrypt the tenant's secret key (AES-256-GCM via Web Crypto)
      const secretKey = await decrypt(
        registry.getMasterKey(),
        record.secretKeyEnc
      );

      // Verify with @smithy/signature-v4 — throws ProxyError on failure
      const url = new URL(c.req.url);
      await verifySigV4({
        method: c.req.method,
        url,
        headers: new Headers(Object.fromEntries(c.req.raw.headers)),
        parsedAuth,
        secretKey,
      });

      // Inject context for handlers
      const ctx = registry.makeContext(record);
      c.set("tenantCtx", ctx);
      c.set("registry", registry);

      await next();
      // next() runs the handler — return its result transparently
      return c.res;
    });
  };
}
