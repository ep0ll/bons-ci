package digest_test

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
)

func TestFromBytes_IsNotEmpty(t *testing.T) {
	d := digest.FromBytes([]byte("hello"))
	if d == "" {
		t.Error("FromBytes returned empty digest")
	}
	if !strings.HasPrefix(string(d), "sha256:") {
		t.Errorf("expected sha256 prefix, got %q", d)
	}
}

func TestFromString_Deterministic(t *testing.T) {
	d1 := digest.FromString("same input")
	d2 := digest.FromString("same input")
	if d1 != d2 {
		t.Errorf("FromString not deterministic: %v vs %v", d1, d2)
	}
}

func TestFromString_DifferentInputs_DifferentDigests(t *testing.T) {
	d1 := digest.FromString("input-a")
	d2 := digest.FromString("input-b")
	if d1 == d2 {
		t.Error("different inputs should produce different digests")
	}
}

func TestDigest_Validate_WellFormed(t *testing.T) {
	d := digest.FromString("content")
	if err := d.Validate(); err != nil {
		t.Errorf("valid digest failed Validate: %v", err)
	}
}

func TestDigest_Validate_MissingColon(t *testing.T) {
	d := digest.Digest("sha256abc")
	if err := d.Validate(); err == nil {
		t.Error("malformed digest should fail Validate")
	}
}

func TestDigest_Validate_EmptyAlgorithm(t *testing.T) {
	d := digest.Digest(":abc123")
	if err := d.Validate(); err == nil {
		t.Error("empty algorithm should fail Validate")
	}
}

func TestDigest_Algorithm(t *testing.T) {
	d := digest.FromString("algo test")
	if d.Algorithm() != "sha256" {
		t.Errorf("Algorithm() = %q, want sha256", d.Algorithm())
	}
}

func TestDigest_Hex_IsLowercaseHex(t *testing.T) {
	d := digest.FromString("hex test")
	hex := d.Hex()
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-lowercase-hex char %q in Hex() %q", c, hex)
		}
	}
	if len(hex) != 64 {
		t.Errorf("SHA-256 hex should be 64 chars, got %d", len(hex))
	}
}

func TestDigest_String_IncludesAlgorithmAndHex(t *testing.T) {
	d := digest.FromString("string test")
	s := d.String()
	if !strings.HasPrefix(s, "sha256:") {
		t.Errorf("String() = %q, expected sha256: prefix", s)
	}
	if len(s) != len("sha256:")+64 {
		t.Errorf("String() length = %d, want %d", len(s), len("sha256:")+64)
	}
}

func TestNewDigestFromBytes_MatchesFromBytes(t *testing.T) {
	content := []byte("direct bytes")
	sum := sha256.Sum256(content)
	got := digest.NewDigestFromBytes("sha256", sum[:])
	want := digest.FromBytes(content)
	if got != want {
		t.Errorf("NewDigestFromBytes != FromBytes: %v vs %v", got, want)
	}
}

func TestDigest_EmptyString_IsDistinctFromNonEmpty(t *testing.T) {
	d1 := digest.FromString("")
	d2 := digest.FromString("a")
	if d1 == d2 {
		t.Error("empty and non-empty strings should produce different digests")
	}
}

func BenchmarkFromString(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		digest.FromString("usr/lib/x86_64-linux-gnu/libssl.so.3")
	}
}
