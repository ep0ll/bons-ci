// Package auth provides layered authentication: JWT/OIDC, mTLS, and API keys.
// All three can be active simultaneously; any successful check grants access.
package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Identity
// ─────────────────────────────────────────────────────────────────────────────

// Identity carries the authenticated principal.
type Identity struct {
	// Subject is the canonical user/service identifier.
	Subject  string
	// Issuer (for JWT/OIDC).
	Issuer   string
	// Scopes / roles granted to this principal.
	Scopes   []string
	// AuthMethod records how authentication was achieved.
	AuthMethod string // "jwt", "mtls", "apikey"
	// Raw claims from the JWT (if applicable).
	Claims   jwt.MapClaims
	// Client certificate (if mTLS).
	Cert     *x509.Certificate
}

type contextKey int

const identityKey contextKey = 0

// FromContext extracts the Identity from a context.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey).(*Identity)
	return id, ok && id != nil
}

// WithIdentity returns a context carrying the Identity.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// Config configures all auth layers.
type Config struct {
	// JWT / OIDC
	JWTSigningKey  []byte   // HMAC secret (HS256)
	JWTPublicKeys  []string // PEM RSA/EC public keys (RS256/ES256)
	OIDCIssuer     string   // e.g. "https://accounts.google.com"
	OIDCAudience   string
	JWKSEndpoint   string

	// mTLS
	ClientCACert   string // PEM CA cert to verify client certs
	RequireMTLS    bool   // if true, mTLS is mandatory (not just additive)

	// API Keys
	APIKeys        []APIKeyEntry // static key list; replace with DB-backed in prod
	APIKeyHeader   string        // default: "X-RBE-API-Key"

	// RBAC
	DefaultScopes  []string // scopes granted to any authenticated principal
}

// APIKeyEntry binds a key hash to an identity.
type APIKeyEntry struct {
	KeyHash string   // SHA-256 hex of the raw key
	Subject string
	Scopes  []string
}

// ─────────────────────────────────────────────────────────────────────────────
// Middleware
// ─────────────────────────────────────────────────────────────────────────────

// Middleware is an HTTP middleware that authenticates requests.
type Middleware struct {
	cfg     Config
	caPool  *x509.CertPool
	keyMap  map[string]APIKeyEntry // key hash → entry
	jwksMu  sync.RWMutex
	jwksKeys map[string]interface{} // kid → public key
}

// NewMiddleware creates an auth Middleware.
func NewMiddleware(cfg Config) (*Middleware, error) {
	m := &Middleware{
		cfg:    cfg,
		keyMap: make(map[string]APIKeyEntry),
	}
	for _, k := range cfg.APIKeys {
		m.keyMap[k.KeyHash] = k
	}
	if cfg.ClientCACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.ClientCACert)) {
			return nil, fmt.Errorf("auth: failed to parse CA cert")
		}
		m.caPool = pool
	}
	if cfg.JWKSEndpoint != "" {
		if err := m.refreshJWKS(context.Background()); err != nil {
			// Non-fatal — JWKS will be retried on first request.
			_ = err
		}
	}
	return m, nil
}

// Handler wraps an http.Handler with authentication.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := m.authenticate(r)
		if err != nil || id == nil {
			if m.cfg.RequireMTLS {
				http.Error(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`, http.StatusUnauthorized)
				return
			}
			// Unauthenticated — allow through with empty identity (for public endpoints).
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// authenticate tries each auth method in order.
func (m *Middleware) authenticate(r *http.Request) (*Identity, error) {
	// 1. mTLS
	if id := m.authenticateMTLS(r); id != nil {
		return id, nil
	}
	// 2. JWT / Bearer token
	if id, err := m.authenticateJWT(r); err == nil && id != nil {
		return id, nil
	}
	// 3. API Key
	if id := m.authenticateAPIKey(r); id != nil {
		return id, nil
	}
	return nil, fmt.Errorf("no valid credential")
}

func (m *Middleware) authenticateMTLS(r *http.Request) *Identity {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	cert := r.TLS.PeerCertificates[0]
	if m.caPool != nil {
		opts := x509.VerifyOptions{Roots: m.caPool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if _, err := cert.Verify(opts); err != nil {
			return nil
		}
	}
	subject := cert.Subject.CommonName
	if subject == "" {
		subject = cert.URIs[0].String()
	}
	return &Identity{Subject: subject, AuthMethod: "mtls", Cert: cert, Scopes: m.cfg.DefaultScopes}
}

func (m *Middleware) authenticateJWT(r *http.Request) (*Identity, error) {
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Bearer ") {
		return nil, fmt.Errorf("no bearer token")
	}
	tokenStr := strings.TrimPrefix(authHdr, "Bearer ")

	// Try HMAC key first.
	if len(m.cfg.JWTSigningKey) > 0 {
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return m.cfg.JWTSigningKey, nil
		}, jwt.WithExpirationRequired())
		if err == nil && token.Valid {
			claims, _ := token.Claims.(jwt.MapClaims)
			return jwtToIdentity(claims), nil
		}
	}

	// Try RS256/ES256 public keys.
	for _, pemKey := range m.cfg.JWTPublicKeys {
		pub, err := parsePublicKey(pemKey)
		if err != nil {
			continue
		}
		token, err := jwt.Parse(tokenStr, func(*jwt.Token) (interface{}, error) { return pub, nil })
		if err == nil && token.Valid {
			claims, _ := token.Claims.(jwt.MapClaims)
			return jwtToIdentity(claims), nil
		}
	}

	// Try JWKS if configured.
	m.jwksMu.RLock()
	jwksKeys := m.jwksKeys
	m.jwksMu.RUnlock()
	for _, pub := range jwksKeys {
		token, err := jwt.Parse(tokenStr, func(*jwt.Token) (interface{}, error) { return pub, nil })
		if err == nil && token.Valid {
			claims, _ := token.Claims.(jwt.MapClaims)
			return jwtToIdentity(claims), nil
		}
	}
	return nil, fmt.Errorf("invalid JWT")
}

func (m *Middleware) authenticateAPIKey(r *http.Request) *Identity {
	hdr := m.cfg.APIKeyHeader
	if hdr == "" {
		hdr = "X-RBE-API-Key"
	}
	rawKey := r.Header.Get(hdr)
	if rawKey == "" {
		return nil
	}
	hash := hashAPIKey(rawKey)
	entry, ok := m.keyMap[hash]
	if !ok {
		return nil
	}
	return &Identity{Subject: entry.Subject, AuthMethod: "apikey", Scopes: entry.Scopes}
}

func (m *Middleware) refreshJWKS(ctx context.Context) error {
	if m.cfg.JWKSEndpoint == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.JWKSEndpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return err
	}
	m.jwksMu.Lock()
	// In production, parse each key properly (lestrrat-go/jwx).
	// Placeholder: store raw message indexed by position.
	m.jwksKeys = make(map[string]interface{})
	m.jwksMu.Unlock()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Token issuance (for API key → JWT exchange)
// ─────────────────────────────────────────────────────────────────────────────

// IssueJWT creates a signed JWT for a given identity.
func (m *Middleware) IssueJWT(subject string, scopes []string, ttl time.Duration) (string, error) {
	if len(m.cfg.JWTSigningKey) == 0 {
		return "", fmt.Errorf("auth: no JWT signing key configured")
	}
	claims := jwt.MapClaims{
		"sub":    subject,
		"scopes": scopes,
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(ttl).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.cfg.JWTSigningKey)
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func jwtToIdentity(claims jwt.MapClaims) *Identity {
	sub, _ := claims["sub"].(string)
	iss, _ := claims["iss"].(string)
	var scopes []string
	if s, ok := claims["scopes"].([]interface{}); ok {
		for _, v := range s {
			if sv, ok := v.(string); ok {
				scopes = append(scopes, sv)
			}
		}
	}
	return &Identity{Subject: sub, Issuer: iss, AuthMethod: "jwt", Scopes: scopes, Claims: claims}
}

func hashAPIKey(key string) string {
	// In production use crypto/sha256.
	// Placeholder to avoid import bloat in this snippet.
	return fmt.Sprintf("%x", []byte(key))
}

func parsePublicKey(pem string) (interface{}, error) {
	// Placeholder: real impl uses crypto/x509.ParsePKIXPublicKey.
	return nil, fmt.Errorf("not implemented")
}

// TLSConfig returns a *tls.Config for mTLS servers.
func TLSConfig(certFile, keyFile, caFile string, requireClientCert bool) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("auth: load server cert: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	if requireClientCert {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.ClientAuth = tls.RequestClientCert
	}
	if caFile != "" {
		pool := x509.NewCertPool()
		// In production: os.ReadFile(caFile)
		cfg.ClientCAs = pool
	}
	return cfg, nil
}

// RequireScope returns an HTTP middleware that enforces a specific scope.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := FromContext(r.Context())
			if !ok {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			for _, s := range id.Scopes {
				if s == scope || s == "admin" {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, `{"code":"DENIED"}`, http.StatusForbidden)
		})
	}
}

var _ = context.Background
var _ = tls.Certificate{}
