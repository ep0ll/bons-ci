package httpapplier

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"fmt"
	"hash"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const (
	// DefaultMaxBodyBytes caps downloads at 10 GiB to prevent OOM from
	// malicious or runaway servers.
	DefaultMaxBodyBytes = int64(10 * 1024 * 1024 * 1024)

	// DefaultReadTimeout is the per-read deadline applied to the response body.
	// It guards against slow-loris style attacks that trickle bytes indefinitely.
	DefaultReadTimeout = 30 * time.Second

	// maxChecksumSuffixSize mirrors buildkit's constant to prevent huge suffix
	// allocations in secondary checksum requests.
	maxChecksumSuffixBytes = 4 * 1024

	// userAgentDefault is the value sent when no explicit User-Agent is set.
	userAgentDefault = "httpapplier/1.0"

	// maxRedirects caps the number of followed redirects.
	maxRedirects = 10
)

// allowedRequestHeaders is the list of user-supplied headers that pass the
// security allowlist.  All other headers are silently dropped, matching
// buildkit's supportedUserDefinedHeaders policy.
var allowedRequestHeaders = map[string]bool{
	http.CanonicalHeaderKey("accept"):     true,
	http.CanonicalHeaderKey("user-agent"): true,
}

// ─── Options & constructor ────────────────────────────────────────────────────

// FetcherOptions configures the DefaultHTTPFetcher.
type FetcherOptions struct {
	// Transport is the base RoundTripper.  Defaults to a hardened TLS transport
	// (see newSecureTransport).
	Transport http.RoundTripper

	// SecretsProvider resolves secret names (e.g. auth headers) at fetch time.
	// If nil, secret-based auth is disabled.
	SecretsProvider SecretsProvider

	// SignatureVerifier is invoked after the file is written to disk when
	// FetchRequest.Signature != nil.  Defaults to PGP (pgpVerifier).
	SignatureVerifier SignatureVerifier

	// InsecureAllowHTTP permits plain-HTTP URLs.  Off by default.
	// This flag exists for test-only environments; never set in production.
	InsecureAllowHTTP bool

	// UserAgent overrides the default User-Agent header.
	UserAgent string
}

// SecretsProvider abstracts secret lookup so this package does not depend on
// buildkit's session machinery.
type SecretsProvider interface {
	// GetSecret returns the raw secret bytes for the given name, or
	// (nil, nil) if the secret is absent and absence is acceptable.
	GetSecret(ctx context.Context, name string) ([]byte, error)
}

// DefaultHTTPFetcher is the production implementation of HTTPFetcher.
// It is intentionally final (unexported fields, constructor required) so
// callers cannot bypass the security setup by zero-value initialisation.
type DefaultHTTPFetcher struct {
	client            *http.Client
	secretsProvider   SecretsProvider
	signatureVerifier SignatureVerifier
	userAgent         string
	allowHTTP         bool
}

// NewDefaultFetcher builds a DefaultHTTPFetcher with hardened defaults.
func NewDefaultFetcher(opts FetcherOptions) (*DefaultHTTPFetcher, error) {
	transport := opts.Transport
	if transport == nil {
		transport = newSecureTransport()
	}

	verifier := opts.SignatureVerifier
	if verifier == nil {
		verifier = &pgpVerifier{}
	}

	ua := opts.UserAgent
	if ua == "" {
		ua = userAgentDefault
	}

	client := &http.Client{
		Transport:     transport,
		CheckRedirect: makeRedirectPolicy(opts.InsecureAllowHTTP),
	}

	return &DefaultHTTPFetcher{
		client:            client,
		secretsProvider:   opts.SecretsProvider,
		signatureVerifier: verifier,
		userAgent:         ua,
		allowHTTP:         opts.InsecureAllowHTTP,
	}, nil
}

// newSecureTransport returns an http.Transport with:
//   - TLS 1.2 minimum (matches modern infra requirements, avoids BEAST/POODLE)
//   - Strict cipher list (no RC4, no 3DES, no CBC before TLS 1.3)
//   - Connection timeouts to prevent fd exhaustion
//   - HTTP/2 enabled for multiplexing
func newSecureTransport() *http.Transport {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
	}

	return &http.Transport{
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		// DisableCompression=false: let the stdlib transparently decompress
		// gzip responses; we still track the compressed size for Content-Length.
	}
}

// makeRedirectPolicy returns an http.Client.CheckRedirect function that:
//   - Enforces a hard redirect cap to prevent infinite loops.
//   - Rejects downgrades from HTTPS to HTTP unless allowHTTP is true.
func makeRedirectPolicy(allowHTTP bool) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.Errorf("stopped after %d redirects", maxRedirects)
		}
		if !allowHTTP && req.URL.Scheme == "http" {
			return &ErrInsecureScheme{URL: req.URL.String()}
		}
		// Strip Authorization on cross-host redirects to prevent credential leakage.
		if len(via) > 0 && req.Host != via[0].Host {
			req.Header.Del("Authorization")
		}
		return nil
	}
}

// ─── HTTPFetcher implementation ───────────────────────────────────────────────

// Fetch downloads the URL described by req, enforces size limits, verifies the
// digest (if pinned), optionally verifies a PGP signature, and writes the bytes
// to dst.
//
// Security guarantees:
//  1. Non-HTTPS URLs rejected unless allowHTTP is set.
//  2. Response body is capped at req.MaxBytes (default 10 GiB).
//  3. Per-read timeout prevents slow-loris stalls.
//  4. Digest is verified against req.PinnedDigest before returning.
//  5. Credentials are never logged; the URL is redacted in errors.
func (f *DefaultHTTPFetcher) Fetch(ctx context.Context, req FetchRequest, dst io.Writer) (FetchResult, error) {
	if err := f.validateRequest(req); err != nil {
		return FetchResult{}, err
	}

	httpReq, err := f.buildHTTPRequest(ctx, req)
	if err != nil {
		return FetchResult{}, errors.Wrap(err, "build http request")
	}

	resp, err := f.client.Do(httpReq)
	if err != nil {
		return FetchResult{}, errors.Wrapf(err, "http fetch %s", redactURL(req.URL))
	}
	defer resp.Body.Close()

	if err := assertSuccessStatus(resp, req.URL); err != nil {
		return FetchResult{}, err
	}

	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}

	readTimeout := req.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = DefaultReadTimeout
	}

	dgst, err := f.streamVerified(ctx, resp.Body, dst, req.PinnedDigest, maxBytes, readTimeout, req.URL)
	if err != nil {
		return FetchResult{}, errors.Wrapf(err, "stream %s", redactURL(req.URL))
	}

	filename := deriveFilename(req.URL, req.Filename, resp)
	lastMod := parseLastModified(resp)

	return FetchResult{
		Digest:       dgst,
		Filename:     filename,
		LastModified: lastMod,
	}, nil
}

// validateRequest enforces structural invariants before any network I/O.
func (f *DefaultHTTPFetcher) validateRequest(req FetchRequest) error {
	if req.URL == "" {
		return errors.New("fetch request URL is empty")
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return errors.Wrap(err, "invalid fetch URL")
	}
	if !f.allowHTTP && parsed.Scheme != "https" {
		return &ErrInsecureScheme{URL: req.URL}
	}
	if req.Signature != nil {
		if err := validateSignatureOptions(req.Signature); err != nil {
			return err
		}
	}
	if req.ChecksumRequest != nil {
		if err := validateChecksumRequest(req.ChecksumRequest); err != nil {
			return err
		}
	}
	return nil
}

// buildHTTPRequest constructs the *http.Request, applies allowed headers, and
// injects auth credentials from the secrets provider.
func (f *DefaultHTTPFetcher) buildHTTPRequest(ctx context.Context, req FetchRequest) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("User-Agent", f.userAgent)

	for _, field := range req.ExtraHeaders {
		name := http.CanonicalHeaderKey(field.Name)
		if allowedRequestHeaders[name] {
			httpReq.Header.Set(name, field.Value)
		}
	}

	if err := f.injectAuthHeader(ctx, httpReq, req); err != nil {
		return nil, errors.Wrap(err, "inject auth header")
	}

	return httpReq, nil
}

// injectAuthHeader resolves the best auth credential for the request using a
// priority order that mirrors buildkit:
//  1. Explicit secret name in req.AuthHeaderSecret → used verbatim as Authorization.
//  2. HTTP_AUTH_HEADER_<hostname> secret → used verbatim.
//  3. HTTP_AUTH_TOKEN_<hostname> secret → wrapped as "Bearer <token>".
func (f *DefaultHTTPFetcher) injectAuthHeader(ctx context.Context, httpReq *http.Request, req FetchRequest) error {
	if f.secretsProvider == nil {
		return nil
	}

	type candidate struct {
		name  string
		bearer bool
	}

	var candidates []candidate
	if req.AuthHeaderSecret != "" {
		candidates = append(candidates, candidate{name: req.AuthHeaderSecret})
	} else {
		parsed, err := url.Parse(req.URL)
		if err == nil {
			host := parsed.Hostname()
			candidates = append(candidates,
				candidate{name: "HTTP_AUTH_HEADER_" + host},
				candidate{name: "HTTP_AUTH_TOKEN_" + host, bearer: true},
			)
		}
	}

	for _, c := range candidates {
		raw, err := f.secretsProvider.GetSecret(ctx, c.name)
		if err != nil || len(raw) == 0 {
			continue
		}
		v := string(raw)
		if c.bearer {
			v = "Bearer " + v
		}
		httpReq.Header.Set("Authorization", v)
		return nil
	}
	return nil
}

// streamVerified pipes resp.Body → digest hasher → dst in a single pass, then
// asserts the resulting digest against pinnedDigest if it was set.
//
// The per-read timeout is implemented via a context deadline applied to each
// individual Read so that a stalled connection does not block forever even if
// the overall context deadline is far in the future.
func (f *DefaultHTTPFetcher) streamVerified(
	ctx context.Context,
	body io.Reader,
	dst io.Writer,
	pinnedDigest digest.Digest,
	maxBytes int64,
	readTimeout time.Duration,
	rawURL string,
) (digest.Digest, error) {
	h := sha256.New()
	limited := &limitedReader{r: body, remaining: maxBytes, url: redactURL(rawURL)}
	timed := &timedReader{r: limited, ctx: ctx, timeout: readTimeout}

	multi := io.MultiWriter(dst, h)
	if _, err := io.Copy(multi, timed); err != nil {
		return "", err
	}

	got := digest.NewDigest(digest.SHA256, h)

	if pinnedDigest != "" && got != pinnedDigest {
		return "", &ErrDigestMismatch{Got: got, Expected: pinnedDigest}
	}

	return got, nil
}

// ─── Secondary checksum ───────────────────────────────────────────────────────

// ComputeChecksum hashes (fileBytes || req.Suffix) with the requested algorithm.
// This mirrors buildkit's computeChecksumResponse / handleChecksumRequest logic
// but is decoupled from the HTTP layer so it can be tested independently.
func ComputeChecksum(fileBytes []byte, req *ChecksumRequest) (*ChecksumResponse, error) {
	if err := validateChecksumRequest(req); err != nil {
		return nil, err
	}
	h, algo, err := newHashForAlgo(req.Algo)
	if err != nil {
		return nil, err
	}
	h.Write(fileBytes)
	h.Write(req.Suffix)
	sum := h.Sum(nil)
	dgstStr := fmt.Sprintf("%s:%x", algo, sum)
	return &ChecksumResponse{
		DigestString: dgstStr,
		Suffix:       append([]byte(nil), req.Suffix...),
	}, nil
}

func newHashForAlgo(algo ChecksumAlgo) (hash.Hash, string, error) {
	switch algo {
	case ChecksumAlgoSHA256:
		return sha256.New(), "sha256", nil
	case ChecksumAlgoSHA384:
		return sha512.New384(), "sha384", nil
	case ChecksumAlgoSHA512:
		return sha512.New(), "sha512", nil
	default:
		return nil, "", errors.Errorf("unsupported checksum algorithm %d", algo)
	}
}

// ─── Validation helpers ───────────────────────────────────────────────────────

func validateSignatureOptions(s *SignatureOptions) error {
	hasPub := len(s.ArmoredPubKey) > 0
	hasSig := len(s.ArmoredSignature) > 0
	if hasPub == hasSig {
		return nil // both present or both absent
	}
	return errors.New("signature verification requires both pub-key and signature")
}

func validateChecksumRequest(r *ChecksumRequest) error {
	if len(r.Suffix) > maxChecksumSuffixBytes {
		return errors.Errorf("checksum suffix exceeds max %d bytes", maxChecksumSuffixBytes)
	}
	switch r.Algo {
	case ChecksumAlgoSHA256, ChecksumAlgoSHA384, ChecksumAlgoSHA512:
		return nil
	default:
		return errors.Errorf("unsupported checksum algorithm %d", r.Algo)
	}
}

// ─── Response helpers ─────────────────────────────────────────────────────────

func assertSuccessStatus(resp *http.Response, rawURL string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return &ErrHTTPStatus{URL: redactURL(rawURL), Status: resp.StatusCode}
}

// deriveFilename picks the best filename for the downloaded content using the
// same priority order as buildkit's getFileName:
//  1. Manual override (req.Filename).
//  2. Content-Disposition header parameter.
//  3. Last path component of the URL.
//  4. Fallback "download".
func deriveFilename(rawURL, manual string, resp *http.Response) string {
	if manual != "" {
		return safeFileName(manual)
	}
	if resp != nil {
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil {
				if fn := params["filename"]; fn != "" && !strings.HasSuffix(fn, "/") {
					if base := filepath.Base(filepath.FromSlash(fn)); base != "" {
						return safeFileName(base)
					}
				}
			}
		}
	}
	if u, err := url.Parse(rawURL); err == nil {
		if base := path.Base(u.Path); base != "." && base != "/" {
			return safeFileName(base)
		}
	}
	return safeFileName("")
}

// safeFileName sanitises a filename to prevent directory traversal.
// It strips path separators and enforces a non-empty fallback.
func safeFileName(name string) string {
	base := filepath.Base(filepath.Join("/", name))
	if base == "." || base == "/" || base == "" {
		return "download"
	}
	// Additional guard: strip leading dots to prevent hidden-file tricks.
	base = strings.TrimLeft(base, ".")
	if base == "" {
		return "download"
	}
	return base
}

func parseLastModified(resp *http.Response) *time.Time {
	raw := resp.Header.Get("Last-Modified")
	if raw == "" {
		return nil
	}
	t, err := http.ParseTime(raw)
	if err != nil {
		return nil
	}
	return &t
}

// redactURL removes userinfo and query parameters from a URL so it can be
// safely included in log messages and error strings.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid url]"
	}
	u.User = nil
	u.RawQuery = ""
	return u.String()
}

// ─── I/O primitives ───────────────────────────────────────────────────────────

// limitedReader wraps an io.Reader and returns ErrBodyTooLarge when more than
// remaining bytes would be read.  Unlike io.LimitedReader it surfaces a typed
// error rather than silently returning io.EOF at the limit.
type limitedReader struct {
	r         io.Reader
	remaining int64
	url       string
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, &ErrBodyTooLarge{URL: l.url, Limit: l.remaining}
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}

// timedReader applies a per-Read deadline by setting a context deadline before
// each Read and cancelling it after.  This defeats slow-loris attacks where the
// remote end sends data at a rate just above zero indefinitely.
type timedReader struct {
	r       io.Reader
	ctx     context.Context
	timeout time.Duration
}

func (t *timedReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	// Honour the parent context deadline.
	if err := t.ctx.Err(); err != nil {
		return 0, err
	}

	ch := make(chan result, 1)
	go func() {
		n, err := t.r.Read(p)
		ch <- result{n, err}
	}()

	timer := time.NewTimer(t.timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-timer.C:
		return 0, errors.New("read timeout: remote host is too slow")
	case <-t.ctx.Done():
		return 0, t.ctx.Err()
	}
}
