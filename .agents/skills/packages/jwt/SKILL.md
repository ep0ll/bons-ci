---
name: pkg-jwt
description: >
  Exhaustive reference for github.com/golang-jwt/jwt/v5: token generation with RS256/ES256,
  validation, claims design, key rotation, refresh token patterns, and security hardening.
  Never use HS256 for distributed systems. Cross-references: security/SKILL.md.
---

# Package: golang-jwt/jwt/v5 — Complete Reference

## Import
```go
import "github.com/golang-jwt/jwt/v5"
```

## 1. Key Loading

```go
// Load RSA keys (production standard)
func LoadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
    data, err := os.ReadFile(path)
    if err != nil { return nil, fmt.Errorf("read private key: %w", err) }
    key, err := jwt.ParseRSAPrivateKeyFromPEM(data)
    if err != nil { return nil, fmt.Errorf("parse RSA private key: %w", err) }
    return key, nil
}

func LoadRSAPublicKey(path string) (*rsa.PublicKey, error) {
    data, err := os.ReadFile(path)
    if err != nil { return nil, fmt.Errorf("read public key: %w", err) }
    key, err := jwt.ParseRSAPublicKeyFromPEM(data)
    if err != nil { return nil, fmt.Errorf("parse RSA public key: %w", err) }
    return key, nil
}

// ECDSA (faster, smaller tokens — preferred for mobile)
func LoadECPrivateKey(path string) (*ecdsa.PrivateKey, error) {
    data, _ := os.ReadFile(path)
    return jwt.ParseECPrivateKeyFromPEM(data)
}
```

## 2. Custom Claims

```go
// Always embed jwt.RegisteredClaims — provides iss, sub, aud, exp, nbf, iat, jti
type AccessClaims struct {
    jwt.RegisteredClaims
    UserID    string   `json:"uid"`
    Role      string   `json:"role"`
    SessionID string   `json:"sid"`
    Scopes    []string `json:"scp,omitempty"`
}

type RefreshClaims struct {
    jwt.RegisteredClaims
    UserID    string `json:"uid"`
    SessionID string `json:"sid"`
    Family    string `json:"fam"` // token family for rotation
}
```

## 3. Token Generation

```go
type TokenService struct {
    privateKey *rsa.PrivateKey
    publicKey  *rsa.PublicKey
    issuer     string
    accessTTL  time.Duration
    refreshTTL time.Duration
}

func (s *TokenService) GenerateAccessToken(userID, role, sessionID string, scopes []string) (string, error) {
    now := time.Now().UTC()
    claims := AccessClaims{
        RegisteredClaims: jwt.RegisteredClaims{
            Issuer:    s.issuer,
            Subject:   userID,
            Audience:  jwt.ClaimStrings{"api"},
            IssuedAt:  jwt.NewNumericDate(now),
            NotBefore: jwt.NewNumericDate(now),
            ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
            ID:        uuid.New().String(), // jti — unique per token
        },
        UserID:    userID,
        Role:      role,
        SessionID: sessionID,
        Scopes:    scopes,
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
    signed, err := token.SignedString(s.privateKey)
    if err != nil { return "", fmt.Errorf("TokenService.GenerateAccessToken: %w", err) }
    return signed, nil
}
```

## 4. Token Validation

```go
func (s *TokenService) ValidateAccessToken(tokenStr string) (*AccessClaims, error) {
    token, err := jwt.ParseWithClaims(tokenStr, &AccessClaims{},
        func(token *jwt.Token) (any, error) {
            // CRITICAL: verify signing method — prevents algorithm confusion attacks
            if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
                return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
            }
            return s.publicKey, nil
        },
        // Parser options — explicit and strict
        jwt.WithValidMethods([]string{"RS256"}),
        jwt.WithIssuedAt(),
        jwt.WithExpirationRequired(),
        jwt.WithAudience("api"),
        jwt.WithIssuer(s.issuer),
    )
    if err != nil {
        // Map jwt errors to domain errors
        switch {
        case errors.Is(err, jwt.ErrTokenExpired):    return nil, ErrTokenExpired
        case errors.Is(err, jwt.ErrTokenNotValidYet): return nil, ErrTokenNotYetValid
        case errors.Is(err, jwt.ErrTokenMalformed):   return nil, ErrTokenMalformed
        default:                                       return nil, ErrTokenInvalid
        }
    }

    claims, ok := token.Claims.(*AccessClaims)
    if !ok || !token.Valid {
        return nil, ErrTokenInvalid
    }
    return claims, nil
}

// Domain errors for JWT (map to HTTP 401 in handler)
var (
    ErrTokenExpired     = errors.New("token expired")
    ErrTokenNotYetValid = errors.New("token not yet valid")
    ErrTokenMalformed   = errors.New("token malformed")
    ErrTokenInvalid     = errors.New("token invalid")
)
```

## 5. Refresh Token Rotation

```go
// Refresh token family: detect token reuse (stolen token detection)
func (s *TokenService) RefreshTokens(ctx context.Context, refreshToken string) (access, refresh string, err error) {
    claims, err := s.validateRefreshToken(refreshToken)
    if err != nil { return "", "", fmt.Errorf("invalid refresh token: %w", err) }

    // Check token family — if family was already rotated, revoke entire family
    used, err := s.tokenStore.IsUsed(ctx, claims.ID) // jti check
    if err != nil { return "", "", err }
    if used {
        // Token reuse detected — invalidate entire session family
        _ = s.tokenStore.RevokeFamily(ctx, claims.Family)
        return "", "", ErrTokenReuse
    }

    // Mark old token as used
    _ = s.tokenStore.MarkUsed(ctx, claims.ID, claims.ExpiresAt.Time)

    // Issue new pair
    access, err = s.GenerateAccessToken(claims.UserID, claims.Role, claims.SessionID, nil)
    if err != nil { return "", "", err }

    refresh, err = s.generateRefreshToken(claims.UserID, claims.SessionID, claims.Family)
    return access, refresh, err
}
```

## 6. Key Rotation Support

```go
// JWKS endpoint: serve public keys for external validators
type JWKS struct {
    Keys []JSONWebKey `json:"keys"`
}
type JSONWebKey struct {
    KID string `json:"kid"`
    Kty string `json:"kty"` // "RSA"
    Alg string `json:"alg"` // "RS256"
    Use string `json:"use"` // "sig"
    N   string `json:"n"`   // base64url modulus
    E   string `json:"e"`   // base64url exponent
}

// Sign tokens with key ID in header
token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
token.Header["kid"] = currentKeyID  // allows validators to select correct public key
```

## JWT Checklist
- [ ] RS256 or ES256 only — never HS256 in distributed systems
- [ ] Algorithm verified in key function: `if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok { return nil, err }`
- [ ] `WithValidMethods([]string{"RS256"})` parser option set
- [ ] `jti` (JWT ID) claim set with UUID — enables token revocation
- [ ] `aud` claim set and validated — prevents token misuse across services
- [ ] Refresh token rotation with family tracking (stolen token detection)
- [ ] JWKS endpoint for public key distribution (key rotation)
- [ ] Token errors mapped to domain errors — not raw jwt.Err* exposed to API
- [ ] Private key loaded from file/secret store — never hardcoded
- [ ] `exp` required and short (15min access, 7d refresh)
