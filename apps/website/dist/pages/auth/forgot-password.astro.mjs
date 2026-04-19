import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$AuthLayout } from '../../chunks/AuthLayout_YoIcWWLp.mjs';
export { renderers } from '../../renderers.mjs';

const $$ForgotPassword = createComponent(async ($$result, $$props, $$slots) => {
  return renderTemplate`${renderComponent($$result, "AuthLayout", $$AuthLayout, { "title": "Reset password \u2014 Forge CI" }, { "default": async ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="max-w-sm w-full"> <div class="mb-8"> <a href="/auth/login" class="inline-flex items-center gap-2 text-sm font-mono mb-6 transition-colors" style="color:#666666" onmouseover="this.style.color='#FFEE00'" onmouseout="this.style.color='#666666'"> <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="square" stroke-width="2" d="M15 19l-7-7 7-7"></path></svg>
Back to sign in
</a> <h1 style="font-family:'Space Grotesk';font-weight:700;font-size:1.75rem;letter-spacing:-0.04em;color:#F0F0F0;line-height:1.0" class="mb-2">Reset your password</h1> <p class="text-sm" style="color:#AAAAAA">Enter your work email and we'll send you a reset link. Valid for 30 minutes.</p> </div> <!-- Step 1: request --> <div id="step-request"> <form id="reset-form" class="space-y-4" novalidate> <div> <label class="input-label" for="reset-email">Work email</label> <input type="email" id="reset-email" class="input" placeholder="you@company.com" autocomplete="email" required> </div> <div id="reset-error" class="hidden px-3 py-2.5 border-2 text-xs font-mono" style="background:rgba(255,51,51,0.08);border-color:#FF3333;color:#FF3333"></div> <button type="submit" id="reset-btn" class="btn-primary btn-md w-full justify-center">
Send reset link →
</button> </form> <div class="mt-6 pt-5 border-t-2" style="border-color:#2C2C2C"> <div class="text-xs font-mono mb-3" style="color:#666666">Other recovery options</div> <div class="space-y-2"> <a href="/auth/sso" class="flex items-center gap-3 px-4 py-2.5 border-2 text-sm border-transparent transition-all" style="background:#1A1A1A;color:#AAAAAA" onmouseover="this.style.borderColor='#FFEE00';this.style.color='#FFEE00'" onmouseout="this.style.borderColor='transparent';this.style.color='#AAAAAA'"> <svg class="w-4 h-4 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="square" stroke-width="1.75" d="M19 21V5a2 2 0 00-2-2H7a2 2 0 00-2 2v16m14 0h2m-2 0h-5m-9 0H3m2 0h5M9 7h1m-1 4h1m4-4h1m-1 4h1m-5 10v-5a1 1 0 011-1h2a1 1 0 011 1v5m-4 0h4"></path></svg>
Sign in with SSO instead
</a> <button id="passkey-recovery" class="w-full flex items-center gap-3 px-4 py-2.5 border-2 text-sm border-transparent transition-all" style="background:#1A1A1A;color:#AAAAAA" onmouseover="this.style.borderColor='#FFEE00';this.style.color='#FFEE00'" onmouseout="this.style.borderColor='transparent';this.style.color='#AAAAAA'"> <svg class="w-4 h-4 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="square" stroke-width="1.75" d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z"></path></svg>
Sign in with passkey instead
</button> </div> </div> </div> <!-- Step 2: sent --> <div id="step-sent" class="hidden"> <div class="w-14 h-14 border-2 flex items-center justify-center mb-6" style="border-color:#00FF88;background:rgba(0,255,136,0.08)"> <svg class="w-7 h-7" fill="none" viewBox="0 0 24 24" stroke="currentColor" style="color:#00FF88"><path stroke-linecap="square" stroke-width="1.5" d="M3 8l7.89 5.26a2 2 0 002.22 0L21 8M5 19h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"></path></svg> </div> <h2 class="text-xl font-bold mb-2" style="font-family:'Space Grotesk';color:#F0F0F0">Check your inbox</h2> <p class="text-sm mb-6" style="color:#AAAAAA">Ory Kratos sent a recovery link to <strong class="font-mono" style="color:#F0F0F0" id="sent-email"></strong>. Click it to set a new password.</p> <div class="space-y-2"> <a href="/auth/login" class="btn-secondary btn-md w-full justify-center">Back to sign in</a> <button id="resend-btn" class="btn-ghost btn-sm w-full justify-center font-mono text-xs" style="color:#666666">
Resend recovery email
</button> </div> <p class="text-xs font-mono mt-4 text-center" style="color:#3C3C3C">Didn't get it? Check your spam folder or contact <a href="/contact" style="color:#FFEE00">support</a>.</p> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/auth/forgot-password.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/auth/forgot-password.astro";
const $$url = "/auth/forgot-password";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$ForgotPassword,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };
