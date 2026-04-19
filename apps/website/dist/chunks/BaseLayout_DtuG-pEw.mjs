import { c as createAstro, a as createComponent, b as renderTemplate, e as renderSlot, f as renderHead, u as unescapeHTML, d as addAttribute } from './astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import 'clsx';
/* empty css                                   */

var __freeze = Object.freeze;
var __defProp = Object.defineProperty;
var __template = (cooked, raw) => __freeze(__defProp(cooked, "raw", { value: __freeze(cooked.slice()) }));
var _a;
const $$Astro = createAstro("https://forge-ci.dev");
const $$BaseLayout = createComponent(($$result, $$props, $$slots) => {
  const Astro2 = $$result.createAstro($$Astro, $$props, $$slots);
  Astro2.self = $$BaseLayout;
  const {
    title = "Forge CI \u2014 Build at the Speed of Thought",
    description = "The fastest CI/CD platform. 10\xD7 faster builds, Sherlock AI, any cloud. 50K+ teams.",
    image = "/og-default.png",
    noindex = false,
    canonical
  } = Astro2.props;
  const fullTitle = title.includes("Forge CI") ? title : `${title} \u2014 Forge CI`;
  const canonicalURL = canonical ?? new URL(Astro2.url.pathname, "https://forge-ci.dev").href;
  return renderTemplate(_a || (_a = __template(['<html lang="en" data-astro-cid-37fxchfa> <head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><meta http-equiv="X-UA-Compatible" content="IE=edge"><title>', '</title><meta name="description"', '><link rel="canonical"', ">", '<meta property="og:type" content="website"><meta property="og:url"', '><meta property="og:title"', '><meta property="og:description"', '><meta property="og:image"', '><meta property="og:site_name" content="Forge CI"><meta name="twitter:card" content="summary_large_image"><meta name="twitter:site" content="@forgeci"><meta name="twitter:title"', '><meta name="twitter:description"', '><meta name="twitter:image"', '><link rel="icon" type="image/svg+xml" href="/favicon.svg"><link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin><link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500;700&display=swap" rel="stylesheet"><meta name="theme-color" content="#0A0A0A"><meta name="color-scheme" content="dark"><script type="application/ld+json">', "<\/script>", "", "</head> <body data-astro-cid-37fxchfa> ", "   </body> </html>"])), fullTitle, addAttribute(description, "content"), addAttribute(canonicalURL, "href"), noindex && renderTemplate`<meta name="robots" content="noindex,nofollow">`, addAttribute(canonicalURL, "content"), addAttribute(fullTitle, "content"), addAttribute(description, "content"), addAttribute(`https://forge-ci.dev${image}`, "content"), addAttribute(fullTitle, "content"), addAttribute(description, "content"), addAttribute(`https://forge-ci.dev${image}`, "content"), unescapeHTML(JSON.stringify({
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    "name": "Forge CI",
    "description": description,
    "url": "https://forge-ci.dev",
    "applicationCategory": "DeveloperApplication",
    "operatingSystem": "Web",
    "offers": { "@type": "Offer", "price": "0", "priceCurrency": "USD" },
    "provider": { "@type": "Organization", "name": "Forge CI, Inc.", "url": "https://forge-ci.dev" }
  })), renderSlot($$result, $$slots["head"]), renderHead(), renderSlot($$result, $$slots["default"]));
}, "/Users/sai/vscode/bons-ci/apps/website/src/layouts/BaseLayout.astro", void 0);

export { $$BaseLayout as $ };
