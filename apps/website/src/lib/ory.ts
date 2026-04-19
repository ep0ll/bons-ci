/**
 * Forge CI — Ory Identity Stack
 * Ory Kratos (authn), Ory Hydra (OAuth2/OIDC), Ory Keto (authz)
 *
 * Production config: point KRATOS_URL, HYDRA_URL, KETO_URL at your
 * Ory Network project (https://console.ory.sh) or self-hosted instances.
 */

// ─── Environment ──────────────────────────────────────────────
export const ORY_CONFIG = {
  kratosPublic: import.meta.env.KRATOS_PUBLIC_URL  ?? 'https://your-project.projects.oryapis.com',
  kratosAdmin:  import.meta.env.KRATOS_ADMIN_URL   ?? 'http://kratos:4434',
  hydraPublic:  import.meta.env.HYDRA_PUBLIC_URL   ?? 'https://your-project.projects.oryapis.com/oauth2',
  hydraAdmin:   import.meta.env.HYDRA_ADMIN_URL    ?? 'http://hydra:4445',
  ketoRead:     import.meta.env.KETO_READ_URL      ?? 'http://keto:4466',
  ketoWrite:    import.meta.env.KETO_WRITE_URL     ?? 'http://keto:4467',
  kratosUi:     import.meta.env.KRATOS_UI_URL      ?? 'https://your-project.projects.oryapis.com/ui',
  selfServiceBase: import.meta.env.SELF_SERVICE_URL ?? 'https://forge-ci.dev',
  sessionCookieName: 'ory_session',
  csrfCookieName:    'ory_csrf_token',
};

// ─── Kratos Flow Types ────────────────────────────────────────
export type FlowType = 'login' | 'registration' | 'recovery' | 'verification' | 'settings';
export type AuthMethod = 'password' | 'oidc' | 'totp' | 'webauthn' | 'lookup_secret' | 'link';

export interface KratosFlow {
  id:           string;
  type:         'browser' | 'api';
  expires_at:   string;
  issued_at:    string;
  request_url:  string;
  ui:           KratosUi;
  state?:       string;
}

export interface KratosUi {
  action:   string;
  method:   string;
  nodes:    KratosNode[];
  messages?: KratosMessage[];
}

export interface KratosNode {
  type:       'input' | 'img' | 'text' | 'a' | 'script';
  group:      string;
  attributes: Record<string, unknown>;
  messages:   KratosMessage[];
  meta:       { label?: { id: number; text: string; type: string } };
}

export interface KratosMessage {
  id:      number;
  type:    'error' | 'info' | 'success';
  text:    string;
  context?: Record<string, unknown>;
}

export interface KratosSession {
  id:          string;
  active:      boolean;
  expires_at:  string;
  authenticated_at: string;
  authenticator_assurance_level: 'aal0' | 'aal1' | 'aal2';
  authentication_methods: Array<{ method: AuthMethod; aal: string; completed_at: string }>;
  identity:    KratosIdentity;
}

export interface KratosIdentity {
  id:           string;
  schema_id:    string;
  schema_url:   string;
  state:        'active' | 'inactive';
  state_changed_at: string;
  traits:       IdentityTraits;
  verifiable_addresses: VerifiableAddress[];
  recovery_addresses:   RecoveryAddress[];
  metadata_public?: Record<string, unknown>;
  credentials?: Record<string, KratosCredential>;
}

export interface IdentityTraits {
  email:     string;
  name?:     { first?: string; last?: string };
  avatar?:   string;
  org_slug?: string;
  role?:     OrgRole;
}

export interface VerifiableAddress {
  id:         string;
  value:      string;
  verified:   boolean;
  via:        'email' | 'phone';
  status:     'pending' | 'sent' | 'completed';
  verified_at?: string;
}

export interface RecoveryAddress {
  id:    string;
  value: string;
  via:   'email' | 'phone';
}

export interface KratosCredential {
  type:        AuthMethod;
  identifiers: string[];
  config?:     Record<string, unknown>;
  created_at:  string;
  updated_at:  string;
}

// ─── Hydra OAuth2 Types ───────────────────────────────────────
export interface OAuth2Client {
  client_id:      string;
  client_name:    string;
  client_secret?: string;
  redirect_uris:  string[];
  grant_types:    string[];
  response_types: string[];
  scope:          string;
  token_endpoint_auth_method: string;
  logo_uri?:      string;
  policy_uri?:    string;
  tos_uri?:       string;
}

export interface OAuth2Token {
  access_token:  string;
  token_type:    string;
  expires_in:    number;
  refresh_token?: string;
  id_token?:     string;
  scope:         string;
}

export interface OAuth2LoginChallenge {
  challenge:        string;
  client:           OAuth2Client;
  requested_scope:  string[];
  requested_access_token_audience: string[];
  skip:             boolean;
  subject?:         string;
}

export interface OAuth2ConsentChallenge {
  challenge:       string;
  client:          OAuth2Client;
  requested_scope: string[];
  subject:         string;
  skip:            boolean;
}

// ─── Keto Permission Types ────────────────────────────────────
export type Namespace  = 'organizations' | 'projects' | 'pipelines' | 'secrets' | 'runners';
export type OrgRole    = 'owner' | 'admin' | 'member' | 'viewer';
export type Permission = 'read' | 'write' | 'delete' | 'admin' | 'trigger' | 'deploy';

export interface KetoRelationTuple {
  namespace:  Namespace;
  object:     string;
  relation:   Permission;
  subject_id?: string;
  subject_set?: { namespace: string; object: string; relation: string };
}

export interface KetoCheckResponse {
  allowed: boolean;
}

// ─── Kratos API Client ────────────────────────────────────────
export class KratosClient {
  private base: string;

  constructor(baseUrl = ORY_CONFIG.kratosPublic) {
    this.base = baseUrl;
  }

  private async fetch<T>(path: string, opts?: RequestInit): Promise<T> {
    const res = await fetch(`${this.base}${path}`, {
      ...opts,
      headers: { 'Accept': 'application/json', 'Content-Type': 'application/json', ...opts?.headers },
      credentials: 'include',
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: { message: res.statusText } }));
      throw new KratosError(err.error?.message ?? 'Kratos error', res.status, err);
    }
    return res.json();
  }

  // ── Flows ──────────────────────────────────────────────────
  async initLoginFlow(opts?: { refresh?: boolean; aal?: 'aal1' | 'aal2'; returnTo?: string }): Promise<KratosFlow> {
    const p = new URLSearchParams();
    if (opts?.refresh)  p.set('refresh', 'true');
    if (opts?.aal)      p.set('aal', opts.aal);
    if (opts?.returnTo) p.set('return_to', opts.returnTo);
    return this.fetch(`/self-service/login/browser?${p}`);
  }

  async initRegistrationFlow(opts?: { returnTo?: string }): Promise<KratosFlow> {
    const p = new URLSearchParams();
    if (opts?.returnTo) p.set('return_to', opts.returnTo);
    return this.fetch(`/self-service/registration/browser?${p}`);
  }

  async initRecoveryFlow(): Promise<KratosFlow> {
    return this.fetch('/self-service/recovery/browser');
  }

  async initVerificationFlow(): Promise<KratosFlow> {
    return this.fetch('/self-service/verification/browser');
  }

  async initSettingsFlow(): Promise<KratosFlow> {
    return this.fetch('/self-service/settings/browser');
  }

  async getFlow(type: FlowType, id: string): Promise<KratosFlow> {
    return this.fetch(`/self-service/${type}/flows?id=${id}`);
  }

  // ── Submit forms ───────────────────────────────────────────
  async submitLogin(action: string, body: Record<string, unknown>): Promise<{ session?: KratosSession; redirect_browser_to?: string }> {
    return this.fetch(action.replace(this.base, ''), {
      method: 'POST',
      body: JSON.stringify(body),
    });
  }

  async submitRegistration(action: string, body: Record<string, unknown>): Promise<{ session?: KratosSession }> {
    return this.fetch(action.replace(this.base, ''), {
      method: 'POST',
      body: JSON.stringify(body),
    });
  }

  async submitSettings(action: string, body: Record<string, unknown>): Promise<{ flow?: KratosFlow }> {
    return this.fetch(action.replace(this.base, ''), {
      method: 'POST',
      body: JSON.stringify(body),
    });
  }

  // ── Session ─────────────────────────────────────────────────
  async getSession(cookie?: string): Promise<KratosSession> {
    return this.fetch('/sessions/whoami', {
      headers: cookie ? { Cookie: cookie } : {},
    });
  }

  async toSession(token?: string): Promise<KratosSession> {
    return this.fetch('/sessions/whoami', {
      headers: token ? { 'X-Session-Token': token } : {},
    });
  }

  async deleteSessions(identityId: string): Promise<void> {
    await this.fetch(`/admin/identities/${identityId}/sessions`, { method: 'DELETE' });
  }

  // ── WebAuthn / Passkey ─────────────────────────────────────
  async getWebAuthnJavascript(): Promise<string> {
    const res = await fetch(`${this.base}/.well-known/ory/webauthn.js`);
    return res.text();
  }

  // ── TOTP ────────────────────────────────────────────────────
  async getTOTPQR(flowId: string): Promise<{ secret: string; qr_uri: string }> {
    // TOTP setup is handled via settings flow nodes
    const flow = await this.getFlow('settings', flowId);
    const totpNode = flow.ui.nodes.find(n => n.group === 'totp' && (n.attributes as any).id === 'totp_secret_key');
    return {
      secret:  (totpNode?.attributes as any)?.text ?? '',
      qr_uri:  (flow.ui.nodes.find(n => (n.attributes as any).id === 'totp_qr')?.attributes as any)?.src ?? '',
    };
  }

  // ── Logout ──────────────────────────────────────────────────
  async logout(): Promise<void> {
    const { logout_url } = await this.fetch<{ logout_url: string }>('/self-service/logout/browser');
    window.location.href = logout_url;
  }

  // ── OIDC helpers ────────────────────────────────────────────
  getOAuthURL(provider: OAuthProvider, action: string): string {
    return `${action}?provider=${provider}`;
  }

  // ── Identity admin (needs admin key) ───────────────────────
  async listIdentities(): Promise<KratosIdentity[]> {
    return this.fetch('/admin/identities');
  }

  async getIdentity(id: string): Promise<KratosIdentity> {
    return this.fetch(`/admin/identities/${id}`);
  }

  async updateIdentityTraits(id: string, traits: Partial<IdentityTraits>): Promise<KratosIdentity> {
    return this.fetch(`/admin/identities/${id}`, {
      method: 'PUT',
      body: JSON.stringify({ schema_id: 'default', traits }),
    });
  }
}

// ─── Hydra Client ─────────────────────────────────────────────
export class HydraClient {
  private adminBase: string;
  private publicBase: string;

  constructor() {
    this.adminBase  = ORY_CONFIG.hydraAdmin;
    this.publicBase = ORY_CONFIG.hydraPublic;
  }

  private async fetch<T>(base: string, path: string, opts?: RequestInit): Promise<T> {
    const res = await fetch(`${base}${path}`, {
      ...opts,
      headers: { 'Accept': 'application/json', 'Content-Type': 'application/json', ...opts?.headers },
    });
    if (!res.ok) throw new Error(`Hydra error ${res.status}: ${res.statusText}`);
    return res.json();
  }

  // ── Authorization flows ────────────────────────────────────
  async getLoginChallenge(challenge: string): Promise<OAuth2LoginChallenge> {
    return this.fetch(this.adminBase, `/admin/oauth2/auth/requests/login?login_challenge=${challenge}`);
  }

  async acceptLogin(challenge: string, subject: string, remember = false): Promise<{ redirect_to: string }> {
    return this.fetch(this.adminBase, '/admin/oauth2/auth/requests/login/accept', {
      method: 'PUT',
      body: JSON.stringify({ challenge, subject, remember, remember_for: remember ? 86400 : 0 }),
    });
  }

  async rejectLogin(challenge: string, reason: string): Promise<{ redirect_to: string }> {
    return this.fetch(this.adminBase, '/admin/oauth2/auth/requests/login/reject', {
      method: 'PUT',
      body: JSON.stringify({ challenge, error: 'access_denied', error_description: reason }),
    });
  }

  async getConsentChallenge(challenge: string): Promise<OAuth2ConsentChallenge> {
    return this.fetch(this.adminBase, `/admin/oauth2/auth/requests/consent?consent_challenge=${challenge}`);
  }

  async acceptConsent(challenge: string, grantScope: string[], remember = false): Promise<{ redirect_to: string }> {
    return this.fetch(this.adminBase, '/admin/oauth2/auth/requests/consent/accept', {
      method: 'PUT',
      body: JSON.stringify({
        challenge,
        grant_scope: grantScope,
        grant_access_token_audience: [],
        remember,
        remember_for: remember ? 86400 * 30 : 0,
        session: { id_token: {}, access_token: {} },
      }),
    });
  }

  // ── Client management ──────────────────────────────────────
  async createClient(client: Partial<OAuth2Client>): Promise<OAuth2Client> {
    return this.fetch(this.adminBase, '/admin/clients', {
      method: 'POST',
      body: JSON.stringify(client),
    });
  }

  async listClients(): Promise<OAuth2Client[]> {
    return this.fetch(this.adminBase, '/admin/clients');
  }

  async introspectToken(token: string): Promise<{ active: boolean; sub?: string; scope?: string; client_id?: string }> {
    const form = new URLSearchParams({ token });
    const res = await fetch(`${this.adminBase}/admin/oauth2/introspect`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: form,
    });
    return res.json();
  }

  // ── PKCE helpers ───────────────────────────────────────────
  buildAuthURL(opts: {
    clientId:     string;
    redirectUri:  string;
    scope:        string[];
    state:        string;
    codeChallenge:string;
    codeChallengeMethod?: string;
  }): string {
    const p = new URLSearchParams({
      response_type:          'code',
      client_id:              opts.clientId,
      redirect_uri:           opts.redirectUri,
      scope:                  opts.scope.join(' '),
      state:                  opts.state,
      code_challenge:         opts.codeChallenge,
      code_challenge_method:  opts.codeChallengeMethod ?? 'S256',
    });
    return `${this.publicBase}/auth?${p}`;
  }

  async exchangeCode(opts: {
    clientId:    string;
    clientSecret:string;
    code:        string;
    redirectUri: string;
    codeVerifier:string;
  }): Promise<OAuth2Token> {
    const form = new URLSearchParams({
      grant_type:    'authorization_code',
      client_id:     opts.clientId,
      client_secret: opts.clientSecret,
      code:          opts.code,
      redirect_uri:  opts.redirectUri,
      code_verifier: opts.codeVerifier,
    });
    const res = await fetch(`${this.publicBase}/token`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: form,
    });
    return res.json();
  }
}

// ─── Keto Client ──────────────────────────────────────────────
export class KetoClient {
  private readBase:  string;
  private writeBase: string;

  constructor() {
    this.readBase  = ORY_CONFIG.ketoRead;
    this.writeBase = ORY_CONFIG.ketoWrite;
  }

  private async fetch<T>(base: string, path: string, opts?: RequestInit): Promise<T> {
    const res = await fetch(`${base}${path}`, {
      ...opts,
      headers: { 'Content-Type': 'application/json', ...opts?.headers },
    });
    if (!res.ok) throw new Error(`Keto error ${res.status}`);
    return res.json();
  }

  /** Check a single permission */
  async check(tuple: KetoRelationTuple): Promise<boolean> {
    const p = new URLSearchParams({
      namespace:  tuple.namespace,
      object:     tuple.object,
      relation:   tuple.relation,
      ...(tuple.subject_id ? { subject_id: tuple.subject_id } : {}),
    });
    const result = await this.fetch<KetoCheckResponse>(this.readBase, `/relation-tuples/check/openapi?${p}`);
    return result.allowed;
  }

  /** Write a permission tuple */
  async write(tuple: KetoRelationTuple): Promise<void> {
    await this.fetch(this.writeBase, '/admin/relation-tuples', {
      method: 'PUT',
      body: JSON.stringify({
        namespace:   tuple.namespace,
        object:      tuple.object,
        relation:    tuple.relation,
        subject_id:  tuple.subject_id,
        subject_set: tuple.subject_set,
      }),
    });
  }

  /** Delete a permission tuple */
  async delete(tuple: KetoRelationTuple): Promise<void> {
    const p = new URLSearchParams({
      namespace: tuple.namespace,
      object:    tuple.object,
      relation:  tuple.relation,
      ...(tuple.subject_id ? { subject_id: tuple.subject_id } : {}),
    });
    await this.fetch(this.writeBase, `/admin/relation-tuples?${p}`, { method: 'DELETE' });
  }

  /** Expand (list subjects with a permission) */
  async expand(namespace: Namespace, object: string, relation: Permission): Promise<string[]> {
    const p = new URLSearchParams({ namespace, object, relation, max_depth: '5' });
    const result = await this.fetch<{ tree?: { children?: Array<{ subject_id?: string }> } }>(
      this.readBase, `/relation-tuples/expand?${p}`
    );
    return (result.tree?.children ?? []).map(c => c.subject_id ?? '').filter(Boolean);
  }

  // ── Convenience helpers ────────────────────────────────────
  async grantOrgRole(userId: string, orgId: string, role: OrgRole): Promise<void> {
    await this.write({ namespace: 'organizations', object: orgId, relation: 'member', subject_id: userId });
    if (role === 'admin' || role === 'owner') {
      await this.write({ namespace: 'organizations', object: orgId, relation: 'admin', subject_id: userId });
    }
    if (role === 'owner') {
      await this.write({ namespace: 'organizations', object: orgId, relation: 'owner', subject_id: userId });
    }
  }

  async revokeOrgAccess(userId: string, orgId: string): Promise<void> {
    for (const rel of ['member', 'admin', 'owner'] as Permission[]) {
      await this.delete({ namespace: 'organizations', object: orgId, relation: rel, subject_id: userId }).catch(() => {});
    }
  }

  async canUserAccessProject(userId: string, projectId: string, perm: Permission): Promise<boolean> {
    return this.check({ namespace: 'projects', object: projectId, relation: perm, subject_id: userId });
  }
}

// ─── Auth error ───────────────────────────────────────────────
export class KratosError extends Error {
  constructor(message: string, public status: number, public body?: unknown) {
    super(message);
    this.name = 'KratosError';
  }

  isUnauthorized()  { return this.status === 401; }
  isForbidden()     { return this.status === 403; }
  isGone()          { return this.status === 410; }
  isRedirect()      { return this.status === 422; }
}

// ─── PKCE helpers (browser) ──────────────────────────────────
export async function generatePKCE(): Promise<{ verifier: string; challenge: string }> {
  const array   = new Uint8Array(32);
  crypto.getRandomValues(array);
  const verifier = btoa(String.fromCharCode(...array)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  const encoded  = new TextEncoder().encode(verifier);
  const hash     = await crypto.subtle.digest('SHA-256', encoded);
  const challenge = btoa(String.fromCharCode(...new Uint8Array(hash))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return { verifier, challenge };
}

// ─── OAuth providers ──────────────────────────────────────────
export type OAuthProvider = 'github' | 'google' | 'gitlab' | 'microsoft' | 'apple' | 'slack';

export const OAUTH_PROVIDERS: Record<OAuthProvider, { name: string; icon: string; color: string }> = {
  github:    { name: 'GitHub',    icon: 'github',    color: '#24292e' },
  google:    { name: 'Google',    icon: 'google',    color: '#4285F4' },
  gitlab:    { name: 'GitLab',    icon: 'gitlab',    color: '#FC6D26' },
  microsoft: { name: 'Microsoft', icon: 'microsoft', color: '#00A4EF' },
  apple:     { name: 'Apple',     icon: 'apple',     color: '#000000' },
  slack:     { name: 'Slack',     icon: 'slack',     color: '#4A154B' },
};

// ─── SSO providers (SAML via Ory) ─────────────────────────────
export interface SAMLProvider {
  id:          string;
  label:       string;
  provider:    string; // 'okta' | 'azure-ad' | 'google-workspace' | 'onelogin' | 'pingidentity'
  sso_url:     string;
  entity_id:   string;
  certificate: string;
  attribute_map: {
    email:      string;
    first_name?: string;
    last_name?:  string;
    groups?:     string;
  };
}

export const FORGE_SAML_ENDPOINTS = {
  acsUrl:      `${ORY_CONFIG.kratosPublic}/self-service/methods/saml/acs`,
  entityId:    'https://forge-ci.dev/saml/metadata',
  metadataUrl: `${ORY_CONFIG.kratosPublic}/self-service/methods/saml/metadata`,
  sloUrl:      `${ORY_CONFIG.kratosPublic}/self-service/methods/saml/slo`,
};

export const SSO_SETUP_GUIDES: Record<string, { steps: string[]; docs: string }> = {
  okta: {
    steps: [
      'In Okta Admin → Applications → Create App Integration',
      'Choose SAML 2.0 as sign-on method',
      'Set Single sign on URL to the Forge CI ACS URL below',
      'Set Audience URI to the Forge CI Entity ID below',
      'Download IdP metadata XML and paste it into Forge CI',
      'Assign users/groups in Okta → your app → Assignments',
    ],
    docs: 'https://help.okta.com/en-us/content/topics/apps/apps_app_integration_wizard_saml.htm',
  },
  'azure-ad': {
    steps: [
      'Azure AD → Enterprise Applications → New application → Custom',
      'Set up single sign-on → SAML',
      'Basic SAML Configuration: set Identifier and Reply URL from below',
      'Download Federation Metadata XML',
      'Map user.mail → emailAddress claim',
      'Paste the XML into Forge CI settings',
    ],
    docs: 'https://learn.microsoft.com/en-us/azure/active-directory/manage-apps/add-application-portal-setup-sso',
  },
  'google-workspace': {
    steps: [
      'Google Admin → Apps → Web and mobile apps → Add app → SAML',
      'Copy ACS URL and Entity ID from Forge CI into Google',
      'Download IdP metadata (Certificate + SSO URL)',
      'Map "Basic Information → Primary email" to NameID',
      'Turn on access for your org units',
      'Upload the Google metadata into Forge CI',
    ],
    docs: 'https://support.google.com/a/answer/6087519',
  },
};

// ─── Auth RBAC (mirrors Keto schema) ─────────────────────────
const ROLE_RANK: Record<OrgRole, number> = { owner: 4, admin: 3, member: 2, viewer: 1 };

export function hasRole(userRole: OrgRole, minRole: OrgRole): boolean {
  return ROLE_RANK[userRole] >= ROLE_RANK[minRole];
}

const PERMISSION_REQUIRES: Record<string, OrgRole> = {
  'builds:read':       'viewer',
  'builds:write':      'member',
  'builds:cancel':     'member',
  'artifacts:read':    'viewer',
  'cache:read':        'viewer',
  'projects:read':     'viewer',
  'projects:write':    'admin',
  'secrets:read':      'admin',
  'secrets:write':     'admin',
  'members:read':      'member',
  'members:invite':    'admin',
  'members:remove':    'admin',
  'tokens:read':       'member',
  'tokens:write':      'admin',
  'billing:read':      'admin',
  'billing:write':     'owner',
  'sso:read':          'admin',
  'sso:configure':     'owner',
  'org:settings':      'admin',
  'org:delete':        'owner',
  'runners:read':      'member',
  'runners:register':  'admin',
  'registry:read':     'viewer',
  'registry:push':     'member',
  'registry:delete':   'admin',
};

export function can(role: OrgRole, permission: string): boolean {
  const required = PERMISSION_REQUIRES[permission] ?? 'viewer';
  return hasRole(role, required);
}

export function getPermissions(role: OrgRole): string[] {
  return Object.entries(PERMISSION_REQUIRES)
    .filter(([, req]) => hasRole(role, req))
    .map(([perm]) => perm);
}

export const API_SCOPE_DESCRIPTIONS: Record<string, string> = {
  'builds:read':    'Read build history, logs, and artifacts',
  'builds:write':   'Trigger and cancel builds',
  'projects:read':  'List and read projects',
  'projects:write': 'Create and configure projects',
  'secrets:read':   'Read secret names (not values)',
  'secrets:write':  'Create, update, and rotate secrets',
  'metrics:read':   'Access build metrics and analytics',
  'registry:read':  'Pull images from the registry',
  'registry:push':  'Push images to the registry',
  'admin':          'Full administrative access (use with care)',
};

// ─── Session cookie helpers (SSR) ─────────────────────────────
export function getSessionCookie(cookieHeader: string | null): string | null {
  if (!cookieHeader) return null;
  const match = cookieHeader.match(new RegExp(`${ORY_CONFIG.sessionCookieName}=([^;]+)`));
  return match ? match[1] : null;
}

// ─── MFA helpers ─────────────────────────────────────────────
export function needsMFA(session: KratosSession | null): boolean {
  if (!session) return false;
  return session.authenticator_assurance_level === 'aal1' &&
    (session.identity.credentials?.totp !== undefined || session.identity.credentials?.webauthn !== undefined);
}

export function hasTOTP(identity: KratosIdentity): boolean {
  return !!identity.credentials?.totp;
}

export function hasWebAuthn(identity: KratosIdentity): boolean {
  return !!identity.credentials?.webauthn;
}

// ─── Singleton instances (lazy) ───────────────────────────────
let _kratos: KratosClient | null = null;
let _hydra:  HydraClient  | null = null;
let _keto:   KetoClient   | null = null;

export function kratos(): KratosClient  { return _kratos ??= new KratosClient(); }
export function hydra():  HydraClient   { return _hydra  ??= new HydraClient(); }
export function keto():   KetoClient    { return _keto   ??= new KetoClient(); }

// ─── Validation helpers ───────────────────────────────────────
export function isValidEmail(e: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(e);
}
export function isWorkEmail(e: string): boolean {
  const free = new Set(['gmail.com','yahoo.com','hotmail.com','outlook.com','icloud.com','proton.me','pm.me']);
  const domain = e.split('@')[1]?.toLowerCase() ?? '';
  return !free.has(domain);
}
export function isValidSlug(s: string): boolean {
  return /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(s);
}
export function slugify(t: string): string {
  return t.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').replace(/-+/g, '-').replace(/^-|-$/g, '');
}
export function checkPasswordStrength(p: string): number {
  let s = 0;
  if (p.length >= 8)  s++;
  if (p.length >= 12) s++;
  if (/[A-Z]/.test(p) && /[0-9]/.test(p)) s++;
  if (/[^a-zA-Z0-9]/.test(p)) s++;
  return s;
}
export function generateToken(prefix = 'fci'): string {
  const arr = new Uint8Array(24);
  crypto.getRandomValues(arr);
  return `${prefix}_${btoa(String.fromCharCode(...arr)).replace(/[+/=]/g, '').slice(0, 32)}`;
}
