/**
 * auth.ts — thin re-export shim for backward compatibility.
 * All identity logic lives in ory.ts.
 */
export {
  hasRole, can, getPermissions,
  isValidEmail, isWorkEmail, isValidSlug, slugify,
  checkPasswordStrength, generateToken,
  API_SCOPE_DESCRIPTIONS,
  FORGE_SAML_ENDPOINTS as FORGE_CI_SAML_ENDPOINTS,
  SSO_SETUP_GUIDES    as SSO_PROVIDER_GUIDES,
  OAUTH_PROVIDERS,
  KratosClient, HydraClient, KetoClient,
  kratos, hydra, keto,
  type OrgRole, type AuthMethod, type OAuthProvider,
  type KratosSession, type KratosIdentity, type KratosFlow,
  type OAuth2Client, type KetoRelationTuple,
} from './ory.ts';

// Legacy constants kept for backward compat
export const MOCK_CURRENT_USER = {
  id: 'usr_ada_lovelace',
  name: { first: 'Ada', last: 'Lovelace' },
  email: 'ada@acme-corp.io',
  initials: 'AL',
  role: 'owner' as const,
  avatar: null,
  mfa_enabled: true,
  sso_provisioned: false,
};

/** Generate a Kratos-compatible OAuth redirect URL */
export function getOAuthURL(provider: string): string {
  return `/auth/login?provider=${provider}&flow=oidc`;
}

/** Build Hydra authorization endpoint URL with PKCE */
export async function buildOAuthURL(opts: {
  clientId: string; redirectUri: string; scope: string[]; state: string;
}): Promise<string> {
  const arr = new Uint8Array(32);
  crypto.getRandomValues(arr);
  const verifier = btoa(String.fromCharCode(...arr)).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
  const encoded  = new TextEncoder().encode(verifier);
  const hash     = await crypto.subtle.digest('SHA-256', encoded);
  const challenge = btoa(String.fromCharCode(...new Uint8Array(hash))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
  sessionStorage.setItem('pkce_verifier', verifier);
  const p = new URLSearchParams({
    response_type: 'code', client_id: opts.clientId, redirect_uri: opts.redirectUri,
    scope: opts.scope.join(' '), state: opts.state,
    code_challenge: challenge, code_challenge_method: 'S256',
  });
  return `/oauth2/auth?${p}`;
}

export function generateAPIToken(prefix = 'fci'): string {
  const arr = new Uint8Array(24);
  crypto.getRandomValues(arr);
  return `${prefix}_${btoa(String.fromCharCode(...arr)).replace(/[+/=]/g,'').slice(0,32)}`;
}
