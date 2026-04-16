// Package signing provides DSSE envelope signing/verification and concrete
// ECDSA P-256 and Ed25519 implementations. Zero external dependencies.
package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Interfaces
// ─────────────────────────────────────────────────────────────────────────────

// Signer produces a digital signature. Implementations must be concurrency-safe.
type Signer interface {
	// Sign returns the signature bytes over msg (PAE-encoded by the caller).
	Sign(msg []byte) ([]byte, error)
	// KeyID returns a stable identifier for the signing key (may be empty).
	KeyID() string
}

// Verifier checks a signature. Implementations must be concurrency-safe.
type Verifier interface {
	// Verify returns nil when sig is a valid signature over msg.
	Verify(msg, sig []byte) error
	// KeyID returns the identifier for this verification key.
	KeyID() string
}

// KeyPair bundles a Signer with its Verifier.
type KeyPair struct {
	Signer   Signer
	Verifier Verifier
}

// ─────────────────────────────────────────────────────────────────────────────
// DSSE Envelope
// ─────────────────────────────────────────────────────────────────────────────

// Envelope is a DSSE signing envelope.
// Spec: https://github.com/secure-systems-lab/dsse/blob/master/protocol.md
type Envelope struct {
	Payload     string      `json:"payload"`
	PayloadType string      `json:"payloadType"`
	Signatures  []Signature `json:"signatures"`
}

// Signature is one entry in an Envelope's signatures array.
type Signature struct {
	Sig   string `json:"sig"`
	KeyID string `json:"keyid,omitempty"`
}

// PAE computes the Pre-Authentication Encoding:
//
//	"DSSEv1" SP len(payloadType) SP payloadType SP len(payload) SP payload
func PAE(payloadType string, payload []byte) []byte {
	header := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	out := make([]byte, 0, len(header)+len(payload))
	out = append(out, header...)
	out = append(out, payload...)
	return out
}

// NewEnvelope creates an unsigned DSSE Envelope for the given payload.
func NewEnvelope(payloadType string, payload []byte) *Envelope {
	return &Envelope{
		Payload:     base64.StdEncoding.EncodeToString(payload),
		PayloadType: payloadType,
	}
}

// StatementEnvelope creates an in-toto DSSE envelope for a JSON statement.
func StatementEnvelope(statementJSON []byte) *Envelope {
	return NewEnvelope("application/vnd.in-toto+json", statementJSON)
}

// DecodePayload base64-decodes the envelope's payload.
func (e *Envelope) DecodePayload() ([]byte, error) {
	for _, enc := range []base64.Encoding{
		*base64.StdEncoding, *base64.URLEncoding,
		*base64.RawStdEncoding, *base64.RawURLEncoding,
	} {
		enc := enc
		b, err := enc.DecodeString(e.Payload)
		if err == nil {
			return b, nil
		}
	}
	return nil, errors.New("dsse: cannot base64-decode payload")
}

func (e *Envelope) paeBytes() ([]byte, error) {
	payload, err := e.DecodePayload()
	if err != nil {
		return nil, err
	}
	return PAE(e.PayloadType, payload), nil
}

// AddSignature signs the envelope and appends the result to e.Signatures.
func (e *Envelope) AddSignature(s Signer) error {
	pae, err := e.paeBytes()
	if err != nil {
		return err
	}
	sig, err := s.Sign(pae)
	if err != nil {
		return fmt.Errorf("dsse: sign: %w", err)
	}
	e.Signatures = append(e.Signatures, Signature{
		Sig:   base64.StdEncoding.EncodeToString(sig),
		KeyID: s.KeyID(),
	})
	return nil
}

// Verify checks that at least one signature is valid for v.
// Returns the index of the first valid signature, or -1 and an error.
func (e *Envelope) Verify(v Verifier) (int, error) {
	if len(e.Signatures) == 0 {
		return -1, errors.New("dsse: no signatures in envelope")
	}
	pae, err := e.paeBytes()
	if err != nil {
		return -1, err
	}

	var errs []string
	for i, sig := range e.Signatures {
		rawSig, err := decodeSig(sig.Sig)
		if err != nil {
			errs = append(errs, fmt.Sprintf("sig[%d]: decode: %v", i, err))
			continue
		}
		if err := v.Verify(pae, rawSig); err != nil {
			errs = append(errs, fmt.Sprintf("sig[%d]: verify: %v", i, err))
			continue
		}
		return i, nil
	}
	return -1, fmt.Errorf("dsse: no valid signature: %s", strings.Join(errs, "; "))
}

func decodeSig(s string) ([]byte, error) {
	for _, enc := range []base64.Encoding{
		*base64.StdEncoding, *base64.RawStdEncoding,
		*base64.URLEncoding, *base64.RawURLEncoding,
	} {
		enc := enc
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("cannot base64-decode signature")
}

// MarshalJSON serialises the envelope.
func (e *Envelope) MarshalJSON() ([]byte, error) {
	type alias Envelope
	return json.Marshal((*alias)(e))
}

// UnmarshalJSON parses an envelope from JSON.
func (e *Envelope) UnmarshalJSON(data []byte) error {
	type alias Envelope
	return json.Unmarshal(data, (*alias)(e))
}

// ─────────────────────────────────────────────────────────────────────────────
// ECDSA P-256
// ─────────────────────────────────────────────────────────────────────────────

// ECDSASigner signs with ECDSA P-256. The message is SHA-256 hashed before signing.
type ECDSASigner struct {
	key   *ecdsa.PrivateKey
	keyID string
}

// NewECDSASigner creates an ECDSASigner from an existing P-256 private key.
func NewECDSASigner(key *ecdsa.PrivateKey, keyID string) (*ECDSASigner, error) {
	if key == nil {
		return nil, errors.New("ecdsa: nil private key")
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("ecdsa: only P-256 is supported")
	}
	return &ECDSASigner{key: key, keyID: keyID}, nil
}

// GenerateECDSAKeyPair generates a fresh P-256 key pair.
func GenerateECDSAKeyPair(keyID string) (*KeyPair, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ecdsa: generate key: %w", err)
	}
	signer, err := NewECDSASigner(priv, keyID)
	if err != nil {
		return nil, err
	}
	verifier, err := NewECDSAVerifier(&priv.PublicKey, keyID)
	if err != nil {
		return nil, err
	}
	return &KeyPair{Signer: signer, Verifier: verifier}, nil
}

// Sign hashes msg with SHA-256 then returns a 64-byte fixed-size (r‖s) signature.
func (s *ECDSASigner) Sign(msg []byte) ([]byte, error) {
	h := sha256.Sum256(msg)
	r, sv, err := ecdsa.Sign(rand.Reader, s.key, h[:])
	if err != nil {
		return nil, fmt.Errorf("ecdsa: sign: %w", err)
	}
	return encodeECDSASig(r, sv), nil
}

// KeyID returns the key identifier.
func (s *ECDSASigner) KeyID() string { return s.keyID }

// PublicKeyPEM returns the PEM-encoded ECDSA public key.
func (s *ECDSASigner) PublicKeyPEM() ([]byte, error) {
	return marshalECDSAPublicKeyPEM(&s.key.PublicKey)
}

// ECDSAVerifier verifies ECDSA P-256 signatures.
type ECDSAVerifier struct {
	key   *ecdsa.PublicKey
	keyID string
}

// NewECDSAVerifier creates a verifier from a P-256 public key.
func NewECDSAVerifier(key *ecdsa.PublicKey, keyID string) (*ECDSAVerifier, error) {
	if key == nil {
		return nil, errors.New("ecdsa verifier: nil public key")
	}
	return &ECDSAVerifier{key: key, keyID: keyID}, nil
}

// NewECDSAVerifierFromPEM parses a PEM-encoded ECDSA public key.
func NewECDSAVerifierFromPEM(pemData []byte, keyID string) (*ECDSAVerifier, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("ecdsa verifier: invalid PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ecdsa verifier: parse public key: %w", err)
	}
	ecKey, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("ecdsa verifier: not an ECDSA public key")
	}
	return NewECDSAVerifier(ecKey, keyID)
}

// Verify checks sig is a valid ECDSA signature over SHA-256(msg).
func (v *ECDSAVerifier) Verify(msg, sig []byte) error {
	h := sha256.Sum256(msg)
	r, sv, err := decodeECDSASig(sig)
	if err != nil {
		return fmt.Errorf("ecdsa: decode sig: %w", err)
	}
	if !ecdsa.Verify(v.key, h[:], r, sv) {
		return errors.New("ecdsa: signature verification failed")
	}
	return nil
}

// KeyID returns the key identifier.
func (v *ECDSAVerifier) KeyID() string { return v.keyID }

// ─────────────────────────────────────────────────────────────────────────────
// Ed25519
// ─────────────────────────────────────────────────────────────────────────────

// Ed25519Signer signs with Ed25519 (no pre-hashing).
type Ed25519Signer struct {
	key   ed25519.PrivateKey
	keyID string
}

// NewEd25519Signer creates an Ed25519Signer.
func NewEd25519Signer(key ed25519.PrivateKey, keyID string) (*Ed25519Signer, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519: invalid key size %d", len(key))
	}
	return &Ed25519Signer{key: key, keyID: keyID}, nil
}

// GenerateEd25519KeyPair generates a fresh Ed25519 key pair.
func GenerateEd25519KeyPair(keyID string) (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ed25519: generate key: %w", err)
	}
	signer, err := NewEd25519Signer(priv, keyID)
	if err != nil {
		return nil, err
	}
	verifier, err := NewEd25519Verifier(pub, keyID)
	if err != nil {
		return nil, err
	}
	return &KeyPair{Signer: signer, Verifier: verifier}, nil
}

// Sign returns an Ed25519 signature over msg (raw bytes, no pre-hashing).
func (s *Ed25519Signer) Sign(msg []byte) ([]byte, error) {
	return ed25519.Sign(s.key, msg), nil
}

// KeyID returns the key identifier.
func (s *Ed25519Signer) KeyID() string { return s.keyID }

// Ed25519Verifier verifies Ed25519 signatures.
type Ed25519Verifier struct {
	key   ed25519.PublicKey
	keyID string
}

// NewEd25519Verifier creates an Ed25519Verifier.
func NewEd25519Verifier(key ed25519.PublicKey, keyID string) (*Ed25519Verifier, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 verifier: invalid key size %d", len(key))
	}
	return &Ed25519Verifier{key: key, keyID: keyID}, nil
}

// Verify checks sig is a valid Ed25519 signature over msg.
func (v *Ed25519Verifier) Verify(msg, sig []byte) error {
	if !ed25519.Verify(v.key, msg, sig) {
		return errors.New("ed25519: signature verification failed")
	}
	return nil
}

// KeyID returns the key identifier.
func (v *Ed25519Verifier) KeyID() string { return v.keyID }

// ─────────────────────────────────────────────────────────────────────────────
// Multi-signer / AnyVerifier
// ─────────────────────────────────────────────────────────────────────────────

// MultiSigner adds a signature from every wrapped signer to an envelope.
type MultiSigner struct{ signers []Signer }

// NewMultiSigner wraps multiple signers.
func NewMultiSigner(signers ...Signer) *MultiSigner { return &MultiSigner{signers: signers} }

// AddSignatures calls AddSignature for every signer in the set.
func (m *MultiSigner) AddSignatures(e *Envelope) error {
	for _, s := range m.signers {
		if err := e.AddSignature(s); err != nil {
			return fmt.Errorf("multisigner %q: %w", s.KeyID(), err)
		}
	}
	return nil
}

// AnyVerifier succeeds if at least one of the wrapped verifiers accepts the signature.
type AnyVerifier struct{ verifiers []Verifier }

// NewAnyVerifier wraps multiple verifiers with OR semantics.
func NewAnyVerifier(verifiers ...Verifier) *AnyVerifier { return &AnyVerifier{verifiers: verifiers} }

// Verify succeeds when any wrapped verifier accepts the signature.
func (a *AnyVerifier) Verify(msg, sig []byte) error {
	for _, v := range a.verifiers {
		if v.Verify(msg, sig) == nil {
			return nil
		}
	}
	return errors.New("anyverifier: no verifier accepted the signature")
}

// KeyID returns an empty string (not meaningful for a composite verifier).
func (a *AnyVerifier) KeyID() string { return "" }

// ─────────────────────────────────────────────────────────────────────────────
// Key helpers
// ─────────────────────────────────────────────────────────────────────────────

func marshalECDSAPublicKeyPEM(pub *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal ecdsa public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// encodeECDSASig encodes (r,s) as a 64-byte fixed-width big-endian pair.
func encodeECDSASig(r, s *big.Int) []byte {
	out := make([]byte, 64)
	rb, sb := r.Bytes(), s.Bytes()
	copy(out[32-len(rb):32], rb)
	copy(out[64-len(sb):64], sb)
	return out
}

// decodeECDSASig parses a 64-byte ECDSA signature.
func decodeECDSASig(sig []byte) (*big.Int, *big.Int, error) {
	if len(sig) != 64 {
		return nil, nil, fmt.Errorf("ecdsa: expected 64-byte sig, got %d", len(sig))
	}
	return new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]), nil
}
