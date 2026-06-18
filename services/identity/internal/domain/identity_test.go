package domain_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/google/uuid"

	"github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall/messaging"
	"github.com/Jeremy-Luyckfasseel/Pitwall/services/identity/internal/domain"
)

// The canonical person-id pattern (contract/README.md Identifiers): a LOWERCASE
// canonical UUID v4 — version nibble 4, variant nibble 8/9/a/b.
var v4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// NormalizeEmail is the canonical-form rule for the email natural key (Q&A Round 31):
// Identity trims surrounding whitespace and lowercases the whole address so that
// case/whitespace variants of one mailbox resolve to exactly one masterId (AC2).
func TestNormalizeEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"jeremy@example.com", "jeremy@example.com"},     // already canonical
		{"Jeremy@Example.com", "jeremy@example.com"},     // case-folded
		{"  jeremy@example.com  ", "jeremy@example.com"}, // surrounding spaces trimmed
		{"\tFOO@X.COM\n", "foo@x.com"},                   // tabs/newlines + uppercase
		{"", ""},                                         // empty stays empty
		{"   ", ""},                                      // whitespace-only collapses to blank
	}
	for _, c := range cases {
		if got := domain.NormalizeEmail(c.in); got != c.want {
			t.Fatalf("NormalizeEmail(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestResolve_ReusesExistingId(t *testing.T) {
	gen := func() string { t.Fatal("gen must not be called when an id already exists"); return "" }
	got, minted := domain.Resolve("1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21", true, gen)
	if minted {
		t.Fatalf("minted = true; want false (existing email must reuse)")
	}
	if got != "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21" {
		t.Fatalf("masterId = %q; want the existing id", got)
	}
}

func TestResolve_MintsWhenAbsent(t *testing.T) {
	gen := func() string { return "fixed-minted-id" }
	got, minted := domain.Resolve("", false, gen)
	if !minted {
		t.Fatalf("minted = false; want true (unknown email must mint)")
	}
	if got != "fixed-minted-id" {
		t.Fatalf("masterId = %q; want the minted id", got)
	}
}

// A blank existing id with found=true is defensively treated as a miss (mint),
// never a reuse of the empty string.
func TestResolve_BlankExistingMints(t *testing.T) {
	got, minted := domain.Resolve("", true, func() string { return "minted" })
	if !minted || got != "minted" {
		t.Fatalf("Resolve(\"\", true) = (%q, %v); want (\"minted\", true)", got, minted)
	}
}

func TestMintedMasterIdIsLowercaseUUIDv4(t *testing.T) {
	// The production minter is uuid.NewString (v4). Prove it satisfies the strict
	// canonical person-id pattern across many draws.
	for i := 0; i < 1000; i++ {
		id := uuid.NewString()
		if !v4Pattern.MatchString(id) {
			t.Fatalf("uuid.NewString() = %q does not match the canonical masterId v4 pattern", id)
		}
	}
}

func TestBuildResolved_CorrelationAndCausation(t *testing.T) {
	req := domain.LookupRequest{
		RequestID:     "aa11bb22-cc33-4dd4-8ee5-ff6677889900",
		Email:         "jeremy@example.com",
		EnvelopeID:    "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f",
		CorrelationID: "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
	}
	env := domain.BuildResolved("identity.resolved", "identity", "2026-06-15T09:14:02.200Z", req, "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")

	if env.Type != "identity.resolved" {
		t.Fatalf("type = %q; want identity.resolved", env.Type)
	}
	if env.Source != "identity" {
		t.Fatalf("source = %q; want identity", env.Source)
	}
	if env.CorrelationID != req.CorrelationID {
		t.Fatalf("correlationId = %q; want it copied verbatim (%q)", env.CorrelationID, req.CorrelationID)
	}
	if env.CausationID == nil || *env.CausationID != req.EnvelopeID {
		t.Fatalf("causationId = %v; want the request envelope id %q", env.CausationID, req.EnvelopeID)
	}
	data, ok := env.Data.(domain.ResolvedData)
	if !ok {
		t.Fatalf("data is %T; want domain.ResolvedData", env.Data)
	}
	if data.RequestID != req.RequestID || data.Email != req.Email || data.MasterID != "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21" {
		t.Fatalf("data = %+v; want requestId/email echoed + the resolved masterId", data)
	}
}

// The built reply must validate against /contract (envelope + identity.resolved data).
func TestBuildResolved_ValidatesAgainstContract(t *testing.T) {
	contractDir, err := messaging.ResolveContractDir("")
	if err != nil {
		t.Fatalf("locate /contract: %v", err)
	}
	validator, err := messaging.NewValidator(contractDir)
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	req := domain.LookupRequest{
		RequestID:     "aa11bb22-cc33-4dd4-8ee5-ff6677889900",
		Email:         "jeremy@example.com",
		EnvelopeID:    "018f9e2a-7c3d-7b21-9c4e-2a1b3c4d5e6f",
		CorrelationID: "8b2e0d44-1f6a-4b9c-9e23-2c7a1f0b3d55",
	}
	env := domain.BuildResolved("identity.resolved", "identity", "2026-06-15T09:14:02.200Z", req, "1a9f7c20-3e84-4d11-9aa2-7b6c5e4d3f21")
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validator.ValidateEnvelopeBytes(raw); err != nil {
		t.Fatalf("built identity.resolved must validate against /contract: %v", err)
	}
}
