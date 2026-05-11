/**
 * Virtual-hosted style normalisation middleware.
 *
 * AWS S3 supports two URL styles:
 *
 *   Path-style:     s3.example.com/{bucket}/{key}
 *   Virtual-hosted: {bucket}.s3.example.com/{key}
 *
 * This middleware normalises virtual-hosted requests by injecting the
 * bucket name as a Hono route parameter, so all handlers downstream
 * can call `c.req.param("bucket")` regardless of URL style.
 *
 * It also rejects obvious subdomain abuse:
 *   - IP-address subdomains
 *   - Subdomains with more than one label (e.g. x.y.s3.example.com)
 *   - Known non-bucket subdomains (www, api, mail, etc.)
 */

import type { Context, Next } from "hono";
import type { AppConfig }     from "@/env";
import { ProxyError }         from "@/errors";

// Subdomains that definitely aren't bucket names
const RESERVED_SUBDOMAINS = new Set([
  "www", "api", "mail", "smtp", "ftp", "ssh", "vpn", "cdn",
  "admin", "console", "dashboard", "status", "metrics",
]);

export function virtualHostMiddleware(config: AppConfig) {
  return async (c: Context, next: Next): Promise<void> => {
    const host         = c.req.header("host") ?? new URL(c.req.url).hostname;
    const hostLower    = host.toLowerCase().split(":")[0]!; // strip port

    const proxyDomain  = config.proxyDomain.toLowerCase();

    // Check if this is a virtual-hosted request
    const suffix = `.${proxyDomain}`;
    if (!hostLower.endsWith(suffix)) {
      // Path-style or root request — no transformation needed
      await next();
      return;
    }

    const subdomain = hostLower.slice(0, hostLower.length - suffix.length);

    // Reject empty, multi-label, or reserved subdomains
    if (!subdomain) {
      await next();
      return;
    }
    if (subdomain.includes(".")) {
      throw ProxyError.invalidBucketName(
        `Virtual-hosted subdomain must be a single label, got: ${subdomain}`,
      );
    }
    if (RESERVED_SUBDOMAINS.has(subdomain)) {
      throw ProxyError.noSuchBucket(subdomain);
    }
    // Reject IP-address-looking subdomains
    if (/^\d{1,3}$/.test(subdomain)) {
      throw ProxyError.invalidBucketName(`Subdomain looks like an IP octet: ${subdomain}`);
    }

    // Inject bucket into the request context for downstream handlers.
    // We store it as a custom variable since we can't modify Hono's route params.
    c.set("virtualHostBucket" as never, subdomain);

    await next();
  };
}
