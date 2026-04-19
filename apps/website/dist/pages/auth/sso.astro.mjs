import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$AuthLayout } from '../../chunks/AuthLayout_YoIcWWLp.mjs';
export { renderers } from '../../renderers.mjs';

const ORY_CONFIG = {
  kratosPublic: "https://your-project.projects.oryapis.com"};
const FORGE_SAML_ENDPOINTS = {
  acsUrl: `${ORY_CONFIG.kratosPublic}/self-service/methods/saml/acs`,
  entityId: "https://forge-ci.dev/saml/metadata",
  metadataUrl: `${ORY_CONFIG.kratosPublic}/self-service/methods/saml/metadata`,
  sloUrl: `${ORY_CONFIG.kratosPublic}/self-service/methods/saml/slo`
};

const $$Sso = createComponent(async ($$result, $$props, $$slots) => {
  const endpoints = FORGE_SAML_ENDPOINTS;
  return renderTemplate`${renderComponent($$result, "AuthLayout", $$AuthLayout, { "title": "Configure SSO \u2014 Forge CI" }, { "default": async ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="max-w-md w-full"> <div class="mb-6"> <a href="/auth/login" class="inline-flex items-center gap-2 text-sm font-mono mb-6 transition-colors" style="color:#666666" onmouseover="this.style.color='#FFEE00'" onmouseout="this.style.color='#666666'"> <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="square" stroke-width="2" d="M15 19l-7-7 7-7"></path></svg>
Back to sign in
</a> <h1 style="font-family:'Space Grotesk';font-weight:700;font-size:1.75rem;letter-spacing:-0.04em;color:#F0F0F0;line-height:1.0" class="mb-1">Configure SSO</h1> <p class="text-sm" style="color:#AAAAAA">Set up SAML 2.0 via Ory Kratos for your organization.</p> </div> <!-- Provider selector --> <div class="mb-5"> <label class="input-label">Identity provider</label> <select id="provider-select" class="input" onchange="updateGuide(this.value)"> <option value="okta">Okta</option> <option value="azure-ad">Azure AD / Entra ID</option> <option value="google-workspace">Google Workspace</option> <option value="onelogin">OneLogin</option> <option value="pingidentity">PingIdentity</option> <option value="custom">Custom SAML IdP</option> </select> </div> <!-- Setup guide (Ory-powered) --> <div class="card p-4 mb-5" style="border-color:rgba(0,238,255,0.3);background:rgba(0,238,255,0.04)"> <div class="text-xs font-mono font-bold uppercase tracking-widest mb-3" style="color:#00EEFF">◎ Setup guide — <span id="provider-name-label">Okta</span></div> <ol class="space-y-2" id="guide-steps"> <li class="flex items-start gap-2.5 text-xs" style="color:#AAAAAA"><span class="font-mono font-bold" style="color:#00EEFF">1.</span>In Okta Admin → Applications → Create App Integration</li> <li class="flex items-start gap-2.5 text-xs" style="color:#AAAAAA"><span class="font-mono font-bold" style="color:#00EEFF">2.</span>Select SAML 2.0 as sign-on method</li> <li class="flex items-start gap-2.5 text-xs" style="color:#AAAAAA"><span class="font-mono font-bold" style="color:#00EEFF">3.</span>Copy the Forge CI ACS URL and Entity ID below into Okta</li> <li class="flex items-start gap-2.5 text-xs" style="color:#AAAAAA"><span class="font-mono font-bold" style="color:#00EEFF">4.</span>Download the Okta metadata XML and paste below</li> <li class="flex items-start gap-2.5 text-xs" style="color:#AAAAAA"><span class="font-mono font-bold" style="color:#00EEFF">5.</span>Assign users/groups in Okta → your app → Assignments</li> </ol> </div> <!-- Forge CI endpoints --> <div class="card p-4 mb-5"> <div class="text-xs font-mono font-bold uppercase tracking-widest mb-3" style="color:#FFEE00">◆ Forge CI SAML endpoints — copy to your IdP</div> <div class="space-y-3"> ${(() => {
    const epMap = endpoints;
    return [
      { label: "ACS URL (Assertion Consumer Service)", key: "acsUrl" },
      { label: "Entity ID (Audience URI)", key: "entityId" },
      { label: "SAML Metadata URL", key: "metadataUrl" },
      { label: "SLO URL (Single Logout)", key: "sloUrl" }
    ].map((e) => {
      const val = epMap[e.key] ?? "\u2014";
      return renderTemplate`<div> <div class="text-xs font-mono mb-1" style="color:#666666">${e.label}</div> <div class="flex items-center gap-0 border-2" style="border-color:#2C2C2C"> <code class="text-xs font-mono flex-1 px-3 py-2 truncate" style="color:#AAAAAA"> ${val} </code> <button${addAttribute(val, "data-copy")} onclick="navigator.clipboard.writeText(this.getAttribute('data-copy'))" class="btn-secondary btn-sm flex-shrink-0 text-xs font-mono" style="border-radius:0;border-top:none;border-right:none;border-bottom:none">
copy
</button> </div> </div>`;
    });
  })()} </div> </div> <!-- IdP metadata input --> <div class="space-y-4"> <div> <label class="input-label">IdP Metadata URL</label> <input type="url" id="metadata-url" class="input input-mono" placeholder="https://your-idp.com/saml/metadata" autocomplete="off"> <p class="text-xs font-mono mt-1.5" style="color:#666666">We'll fetch and parse automatically from a URL.</p> </div> <div class="relative"> <div class="absolute inset-0 flex items-center"><div class="w-full border-t" style="border-color:#2C2C2C"></div></div> <div class="relative flex justify-center"><span class="px-3 text-xs font-mono" style="background:#0A0A0A;color:#666666">OR PASTE XML</span></div> </div> <div> <label class="input-label">IdP Metadata XML</label> <textarea${addAttribute(4, "rows")} class="input input-mono resize-none text-xs"${addAttribute('<?xml version="1.0"?>\n<md:EntityDescriptor \u2026', "placeholder")}></textarea> </div> <!-- Attribute mapping --> <div class="card p-4"> <div class="text-xs font-mono font-bold uppercase tracking-widest mb-3" style="color:#FFEE00">◈ Attribute mapping</div> <div class="space-y-2"> ${[
    { l: "Email (required)", v: "email", ph: "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress" },
    { l: "First name", v: "firstName", ph: "givenName" },
    { l: "Last name", v: "lastName", ph: "sn" },
    { l: "Groups / Roles", v: "groups", ph: "memberOf" }
  ].map((a) => renderTemplate`<div class="grid grid-cols-[120px_1fr] gap-2 items-center"> <label class="text-xs font-mono" style="color:#666666">${a.l}</label> <input type="text" class="input input-mono text-xs"${addAttribute(a.v, "value")}${addAttribute(a.ph, "placeholder")}> </div>`)} </div> </div> <!-- Enforcement options --> <div class="space-y-3"> ${[
    { l: "Enforce SSO for all members", d: "Disable password login. All members must use SSO.", on: false },
    { l: "Auto-provision new members (SCIM)", d: "Users assigned in your IdP automatically get Forge CI access.", on: false },
    { l: "Auto-deprovision on removal", d: "Deassigning in IdP immediately revokes Forge CI access.", on: false },
    { l: "Sync group roles", d: "Map IdP groups to Forge CI roles (admin, member, viewer).", on: false }
  ].map((s) => renderTemplate`<label class="flex items-center gap-3 cursor-pointer"> <div class="toggle-switch flex-shrink-0" onclick="this.classList.toggle('on')"> <div class="toggle-thumb"></div> </div> <div> <div class="text-sm font-semibold" style="color:#F0F0F0">${s.l}</div> <div class="text-xs" style="color:#666666">${s.d}</div> </div> </label>`)} </div> <button id="test-btn" class="btn-secondary btn-md w-full justify-center"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="square" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z"></path><path stroke-linecap="square" stroke-width="2" d="M21 12a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>
Test SAML connection
</button> <button class="btn-primary btn-md w-full justify-center">Save & enable SSO</button> </div> <!-- Test result --> <div id="test-result" class="hidden mt-4 card p-4 card-green"> <div class="text-xs font-mono font-bold uppercase" style="color:#00FF88">✓ Connection verified</div> <p class="text-xs mt-1" style="color:#AAAAAA">Successfully received SAML response. SSO is ready to enable.</p> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/auth/sso.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/auth/sso.astro";
const $$url = "/auth/sso";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Sso,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };
