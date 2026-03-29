package gitapply

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ─── ChecksumMismatchError ────────────────────────────────────────────────────

func TestChecksumMismatchError_Is(t *testing.T) {
	t.Parallel()
	err := &ChecksumMismatchError{
		ExpectedPrefix: "abc123",
		ActualSHA:      "def456" + "0000000000000000000000000000000000",
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Error("ChecksumMismatchError should satisfy errors.Is(ErrChecksumMismatch)")
	}
	if errors.Is(err, ErrRefNotFound) {
		t.Error("ChecksumMismatchError should not satisfy errors.Is(ErrRefNotFound)")
	}
}

func TestChecksumMismatchError_Message_withAlt(t *testing.T) {
	t.Parallel()
	err := &ChecksumMismatchError{
		ExpectedPrefix: "deadbeef",
		ActualSHA:      "aaaa",
		AltSHA:         "bbbb",
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() must not be empty")
	}
	if !strings.Contains(msg, "deadbeef") {
		t.Errorf("message should mention expected prefix; got: %q", msg)
	}
	if !strings.Contains(msg, "aaaa") || !strings.Contains(msg, "bbbb") {
		t.Errorf("message should mention both SHAs; got: %q", msg)
	}
}

func TestChecksumMismatchError_Message_withoutAlt(t *testing.T) {
	t.Parallel()
	err := &ChecksumMismatchError{
		ExpectedPrefix: "deadbeef",
		ActualSHA:      "cccc",
	}
	msg := err.Error()
	if strings.Contains(msg, "or") && strings.Contains(msg, "bbbb") {
		t.Errorf("message must not mention alt SHA when none present; got: %q", msg)
	}
}

// ─── FetchError ───────────────────────────────────────────────────────────────

func TestFetchError_Unwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("network unreachable")
	fe := newFetchError("https://user:secret@github.com/repo.git", inner)
	if !errors.Is(fe, inner) {
		t.Error("FetchError.Unwrap() should return the inner error")
	}
	if strings.Contains(fe.Error(), "secret") {
		t.Errorf("FetchError.Error() must not expose credentials: %q", fe.Error())
	}
	if !strings.Contains(fe.Error(), "github.com") {
		t.Errorf("FetchError.Error() should contain the redacted host: %q", fe.Error())
	}
}

// ─── classifyFetchError ───────────────────────────────────────────────────────

func TestClassifyFetchError_nil(t *testing.T) {
	t.Parallel()
	if classifyFetchError(nil) != nil {
		t.Error("classifyFetchError(nil) must return nil")
	}
}

func TestClassifyFetchError_wouldClobberTag(t *testing.T) {
	t.Parallel()
	base := fmt.Errorf("rejected (would clobber existing tag)")
	classified := classifyFetchError(base)

	var wce *wouldClobberTagError
	if !errors.As(classified, &wce) {
		t.Errorf("expected wouldClobberTagError; got %T: %v", classified, classified)
	}
	if !errors.Is(classified, base) {
		t.Error("classified error should unwrap to original")
	}
}

func TestClassifyFetchError_unableToUpdateRef(t *testing.T) {
	t.Parallel()
	cases := []string{
		"some local refs could not be updated; try running 'git remote prune origin'",
		"error: cannot lock ref 'refs/foo' (unable to update local ref)",
		"error: refname conflict",
	}
	for _, msg := range cases {
		msg := msg
		t.Run(msg[:20], func(t *testing.T) {
			t.Parallel()
			classified := classifyFetchError(errors.New(msg))
			var ulre *unableToUpdateRefError
			if !errors.As(classified, &ulre) {
				t.Errorf("expected unableToUpdateRefError; got %T: %v", classified, classified)
			}
		})
	}
}

func TestClassifyFetchError_passthrough(t *testing.T) {
	t.Parallel()
	base := errors.New("unrecognised random error")
	classified := classifyFetchError(base)
	if classified != base {
		t.Errorf("unrecognised errors must be returned unchanged; got %v", classified)
	}
}

// ─── scrubAuthArgs ────────────────────────────────────────────────────────────

func TestScrubAuthArgs(t *testing.T) {
	t.Parallel()
	// Build args exactly as gitCLI.run would: base config first, then auth -c, then subcmd.
	//
	//  [0] "fetch"
	//  [1] "-c"          ← flag for extraheader value
	//  [2] "http.https://github.com/.extraheader=Authorization: basic SuperSecretToken"
	//  [3] "origin"
	//  [4] "-c"          ← flag for protocol.version value
	//  [5] "protocol.version=2"
	args := []string{
		"fetch",
		"-c", "http.https://github.com/.extraheader=Authorization: basic SuperSecretToken",
		"origin",
		"-c", "protocol.version=2", // must NOT be scrubbed
	}
	scrubbed := scrubAuthArgs(args)

	if len(scrubbed) != len(args) {
		t.Fatalf("length mismatch: want %d, got %d", len(args), len(scrubbed))
	}
	// Index 2: the extraheader value must be replaced.
	if scrubbed[2] != "<redacted>" {
		t.Errorf("scrubbed[2] (extraheader value) should be <redacted>; got %q", scrubbed[2])
	}
	// Index 4: the "-c" flag preceding protocol.version must be preserved as-is.
	if scrubbed[4] != "-c" {
		t.Errorf("scrubbed[4] (-c flag) should be preserved; got %q", scrubbed[4])
	}
	// Index 5: the protocol.version value must be preserved (no extraheader substring).
	if scrubbed[5] != "protocol.version=2" {
		t.Errorf("scrubbed[5] (protocol.version) should be unchanged; got %q", scrubbed[5])
	}
	// Index 0 and 3: non-auth positional args must be unchanged.
	if scrubbed[0] != "fetch" || scrubbed[3] != "origin" {
		t.Error("non-auth args must not be modified")
	}
	// Verify that scrubAuthArgs does not mutate the input slice.
	if args[2] == "<redacted>" {
		t.Error("scrubAuthArgs must not mutate the input slice")
	}
}

func TestScrubAuthArgs_noAuth(t *testing.T) {
	t.Parallel()
	args := []string{"ls-remote", "--symref", "https://github.com/user/repo.git", "HEAD"}
	scrubbed := scrubAuthArgs(args)
	for i, a := range scrubbed {
		if a != args[i] {
			t.Errorf("arg[%d] changed without auth present: %q → %q", i, args[i], a)
		}
	}
}
