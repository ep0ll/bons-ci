---
name: golang-security
description: >
  Comprehensive Go security: cryptography, authentication, authorization, input validation,
  secrets management, TLS hardening, injection prevention, supply chain security, and
  OWASP Top 10 mitigations. Use for any Go service, API, or tool handling sensitive data,
  user input, network connections, or credentials. Always combine with networking/SKILL.md
  for network services.
---

# Go Security — Comprehensive Hardening Guide

## 1. Cryptography

### Hashing & Key Derivation
```go
import (
    "crypto/rand"
    "crypto/subtle"
    "golang.org/x/crypto/argon2"
    "golang.org/x/crypto/bcrypt"
)

// Password hashing: argon2id (preferred) or bcrypt
func HashPassword(password string) (string, error) {
    // Argon2id — OWASP recommended parameters (2024)
    salt := make([]byte, 16)
    if _, err := rand.Read(salt); err != nil {
        return "", fmt.Errorf("generate salt: %w", err)
    }
    hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
    // encode: base64(salt) + "$" + base64(hash) + params
    encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
        argon2.Version, 64*1024, 3, 4,
        base64.RawStdEncoding.EncodeToString(salt),
        base64.RawStdEncoding.EncodeToString(hash))
    return encoded, nil
}

// Constant-time comparison — ALWAYS for secrets
func CompareSecrets(a, b []byte) bool {
    return subtle.ConstantTimeCompare(a, b) == 1
}

// NEVER use: MD5, SHA1 for security purposes, math/rand for secrets
// ALWAYS use: crypto/rand for all random security tokens
func GenerateToken(n int) (string, error) {
    b := make([]byte, n)
    if _, err := rand.Read(b); err != nil {
        return "", fmt.Errorf("rand.Read: %w", err)
    }
    return base64.URLEncoding.EncodeToString(b), nil
}
```

### Encryption
```go
import "crypto/aes"
import "crypto/cipher"

// AES-GCM (authenticated encryption) — prefer over CBC
func Encrypt(key, plaintext []byte) ([]byte, error) {
    block, err := aes.NewCipher(key) // key must be 16, 24, or 32 bytes
    if err != nil { return nil, fmt.Errorf("aes.NewCipher: %w", err) }
    
    gcm, err := cipher.NewGCM(block)
    if err != nil { return nil, fmt.Errorf("cipher.NewGCM: %w", err) }
    
    nonce := make([]byte, gcm.NonceSize())
    if _, err := rand.Read(nonce); err != nil { return nil, err }
    
    // Prepend nonce to ciphertext
    return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func Decrypt(key, ciphertext []byte) ([]byte, error) {
    block, _ := aes.NewCipher(key)
    gcm, _ := cipher.NewGCM(block)
    nonceSize := gcm.NonceSize()
    if len(ciphertext) < nonceSize {
        return nil, errors.New("ciphertext too short")
    }
    nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
    return gcm.Open(nil, nonce, ciphertext, nil)
}

// Key derivation from passphrase: use HKDF
import "golang.org/x/crypto/hkdf"
func DeriveKey(secret, salt, info []byte, keyLen int) ([]byte, error) {
    r := hkdf.New(sha256.New, secret, salt, info)
    key := make([]byte, keyLen)
    if _, err := io.ReadFull(r, key); err != nil {
        return nil, fmt.Errorf("hkdf.Read: %w", err)
    }
    return key, nil
}
```

---

## 2. TLS Hardening

```go
import "crypto/tls"

// Server TLS config — production hardened
func NewTLSConfig() *tls.Config {
    return &tls.Config{
        MinVersion: tls.VersionTLS13, // TLS 1.3 minimum
        // If TLS 1.2 must be supported:
        // MinVersion: tls.VersionTLS12,
        // CipherSuites: []uint16{
        //     tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
        //     tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
        //     tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
        // },
        CurvePreferences: []tls.CurveID{
            tls.X25519,
            tls.CurveP256,
        },
        PreferServerCipherSuites: true,
        SessionTicketsDisabled: false, // enable for performance, rotate keys
    }
}

// mTLS — mutual TLS for service-to-service
func NewMTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
    cert, err := tls.LoadX509KeyPair(certFile, keyFile)
    if err != nil { return nil, fmt.Errorf("load cert: %w", err) }
    
    caCert, err := os.ReadFile(caFile)
    if err != nil { return nil, fmt.Errorf("read CA: %w", err) }
    caPool := x509.NewCertPool()
    if !caPool.AppendCertsFromPEM(caCert) {
        return nil, errors.New("failed to parse CA certificate")
    }
    
    return &tls.Config{
        Certificates: []tls.Certificate{cert},
        ClientCAs:    caPool,
        ClientAuth:   tls.RequireAndVerifyClientCert,
        MinVersion:   tls.VersionTLS13,
    }, nil
}
```

---

## 3. Input Validation & Injection Prevention

```go
// Allowlist validation — always prefer over denylist
var validUsername = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)
func ValidateUsername(s string) error {
    if !validUsername.MatchString(s) {
        return &ValidationError{Field: "username", Message: "must be 3-32 alphanumeric chars"}
    }
    return nil
}

// SQL injection: always use parameterized queries
// NEVER: fmt.Sprintf("SELECT * FROM users WHERE id = %s", id)
// ALWAYS:
rows, err := db.QueryContext(ctx, "SELECT * FROM users WHERE id = $1", id)

// Path traversal prevention
func SafeJoin(base, userInput string) (string, error) {
    abs, err := filepath.Abs(filepath.Join(base, userInput))
    if err != nil { return "", err }
    if !strings.HasPrefix(abs, base) {
        return "", fmt.Errorf("path traversal detected: %q", userInput)
    }
    return abs, nil
}

// Command injection: NEVER use shell=true equivalents
// NEVER: exec.Command("sh", "-c", "ls " + userInput)
// ALWAYS: pass args separately
cmd := exec.CommandContext(ctx, "ls", "-la", safeDir)

// XML/HTML — use text/template for plain text, html/template for HTML
// html/template auto-escapes; text/template does NOT
import "html/template"
tmpl := template.Must(template.New("page").Parse(`<p>{{.UserInput}}</p>`))
// UserInput is automatically HTML-escaped ✓

// JSON deserialization: use strict decoder
dec := json.NewDecoder(r.Body)
dec.DisallowUnknownFields()
if err := dec.Decode(&req); err != nil { ... }
```

---

## 4. Authentication & Authorization

```go
// JWT: use only asymmetric algorithms in production
import "github.com/golang-jwt/jwt/v5"

// Sign with RS256 or ES256 — never HS256 in distributed systems
func GenerateJWT(privateKey *rsa.PrivateKey, claims jwt.Claims) (string, error) {
    token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
    return token.SignedString(privateKey)
}

func ValidateJWT(tokenStr string, publicKey *rsa.PublicKey) (*jwt.Token, error) {
    return jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
        if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
        }
        return publicKey, nil
    })
}

// Authorization: RBAC middleware
type Role string
const (RoleAdmin Role = "admin"; RoleUser Role = "user")

func RequireRole(roles ...Role) func(http.Handler) http.Handler {
    roleSet := make(map[Role]struct{}, len(roles))
    for _, r := range roles { roleSet[r] = struct{}{} }
    
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            user, ok := UserFromContext(r.Context())
            if !ok { http.Error(w, "unauthorized", http.StatusUnauthorized); return }
            if _, allowed := roleSet[user.Role]; !allowed {
                http.Error(w, "forbidden", http.StatusForbidden); return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

---

## 5. Secrets Management

```go
// NEVER hardcode secrets — use environment or secret stores
// Pattern: SecretProvider interface for pluggability
type SecretProvider interface {
    GetSecret(ctx context.Context, name string) (string, error)
}

// Env-based (dev/simple)
type EnvSecretProvider struct{}
func (e *EnvSecretProvider) GetSecret(_ context.Context, name string) (string, error) {
    v, ok := os.LookupEnv(name)
    if !ok { return "", fmt.Errorf("secret %q not set", name) }
    return v, nil
}

// Vault-based (production)
type VaultSecretProvider struct { client *vault.Client; path string }
func (v *VaultSecretProvider) GetSecret(ctx context.Context, name string) (string, error) {
    secret, err := v.client.KVv2(v.path).Get(ctx, name)
    if err != nil { return "", fmt.Errorf("vault.Get(%s): %w", name, err) }
    val, ok := secret.Data["value"].(string)
    if !ok { return "", fmt.Errorf("secret %q missing 'value' key", name) }
    return val, nil
}

// Zeroise secrets from memory after use
func ZeroBytes(b []byte) { for i := range b { b[i] = 0 } }
```

---

## 6. HTTP Security Headers

```go
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        h := w.Header()
        h.Set("X-Content-Type-Options", "nosniff")
        h.Set("X-Frame-Options", "DENY")
        h.Set("X-XSS-Protection", "0") // modern browsers use CSP instead
        h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
        h.Set("Content-Security-Policy",
            "default-src 'self'; script-src 'self'; object-src 'none'")
        h.Set("Strict-Transport-Security",
            "max-age=63072000; includeSubDomains; preload")
        h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
        next.ServeHTTP(w, r)
    })
}
```

---

## 7. Rate Limiting & DoS Prevention

```go
import "golang.org/x/time/rate"

// Per-IP rate limiter with cleanup
type IPRateLimiter struct {
    limiters sync.Map
    rate     rate.Limit
    burst    int
}

func (l *IPRateLimiter) Allow(ip string) bool {
    v, _ := l.limiters.LoadOrStore(ip,
        rate.NewLimiter(l.rate, l.burst))
    return v.(*rate.Limiter).Allow()
}

// Request size limits (prevent billion-laugh / large payload attacks)
r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
```

---

## 8. Supply Chain Security

```go
// go.sum verification: ALWAYS commit go.sum
// Use GONOSUMCHECK only for private modules
// Verify checksums in CI:
// GONOSUMCHECK="" GOFLAGS="-mod=readonly" go build ./...

// Dependency audit in CI:
// govulncheck ./...  — official Go vulnerability scanner
// nancy / osv-scanner for additional CVE coverage

// Minimal Docker base image — reduces attack surface
// FROM gcr.io/distroless/static-debian12:nonroot
// or: FROM scratch (for static binaries)
```

---

## Security Checklist

- [ ] No secrets in source code, logs, or error messages
- [ ] All crypto uses `crypto/rand`, not `math/rand`
- [ ] Passwords hashed with argon2id/bcrypt — never stored plaintext
- [ ] All comparisons of secrets use `subtle.ConstantTimeCompare`
- [ ] TLS 1.3 minimum, certificate validation enabled
- [ ] All SQL uses parameterized queries
- [ ] All user input validated with allowlists
- [ ] Path traversal checks on all file operations
- [ ] Rate limiting on all public endpoints
- [ ] Security headers on all HTTP responses
- [ ] `govulncheck` passes in CI pipeline
- [ ] No `exec.Command` with user input in shell form
