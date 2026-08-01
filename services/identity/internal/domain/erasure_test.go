package domain_test

import (
	"regexp"
	"testing"

	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
)

var hexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HashEmail (Round 33/Q33.1): a deterministic, irreversible SHA-256 hash of an
// already-normalized email — the suppression key that survives erasure without
// retaining the plaintext PII.
func TestHashEmail_DeterministicForSameInput(t *testing.T) {
	a := domain.HashEmail("jeremy@example.com")
	b := domain.HashEmail("jeremy@example.com")
	if a != b {
		t.Fatalf("HashEmail not deterministic: %q != %q", a, b)
	}
}

func TestHashEmail_DifferentInputsDifferentHashes(t *testing.T) {
	a := domain.HashEmail("jeremy@example.com")
	b := domain.HashEmail("someone-else@example.com")
	if a == b {
		t.Fatal("two different emails hashed to the same value")
	}
}

func TestHashEmail_OutputShapeIsLowercaseHexSHA256(t *testing.T) {
	got := domain.HashEmail("jeremy@example.com")
	if !hexPattern.MatchString(got) {
		t.Fatalf("HashEmail output %q is not 64 lowercase hex chars", got)
	}
}

// Callers must normalize first (Round 31) — HashEmail itself does not, so a caller that
// forgets to normalize produces a DIFFERENT hash for case/whitespace variants of the
// same mailbox. This test documents that contract rather than "fixing" it inside
// HashEmail, which stays a pure, single-purpose function.
func TestHashEmail_DoesNotNormalizeItself(t *testing.T) {
	lower := domain.HashEmail("jeremy@example.com")
	upper := domain.HashEmail("Jeremy@Example.com")
	if lower == upper {
		t.Fatal("HashEmail must not itself normalize case — callers must call NormalizeEmail first")
	}
}
