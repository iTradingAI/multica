package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFileEvidenceClaimDigestJSONBRoundTrip(t *testing.T) {
	a, err := FileEvidenceClaimDigest([]byte(`{"generation":9007199254740993,"root":"fixture","nested":{"b":2,"a":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := FileEvidenceClaimDigest([]byte(`{ "nested": {"a":1, "b":2}, "root":"fixture", "generation":9007199254740993 }`))
	if err != nil || a != b {
		t.Fatal("JSONB formatting changed claim binding")
	}
	c, _ := FileEvidenceClaimDigest([]byte(`{"generation":9007199254740992,"root":"fixture","nested":{"a":1,"b":2}}`))
	if a == c {
		t.Fatal("large integer generations collapsed")
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`} {
		if _, err := FileEvidenceClaimDigest([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed claim %s", raw)
		}
	}
}

func TestFileEvidenceTokenBindingAndEntropy(t *testing.T) {
	digest, _ := FileEvidenceClaimDigest([]byte(`{"proof_version":1}`))
	scope := FileEvidenceScope{WorkspaceID: uuid.NewString(), TaskID: uuid.NewString(), RuntimeID: uuid.NewString(), DaemonID: uuid.NewString(), DispatchedAt: time.Now().UTC().Format(time.RFC3339Nano), ClaimDigest: digest, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	first, err := GenerateFileEvidenceToken(scope)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := GenerateFileEvidenceToken(scope)
	if first == second || HashToken(first) == HashToken(second) {
		t.Fatal("repeated claim reused evidence credential")
	}
	parsed, err := ParseFileEvidenceToken(first)
	if err != nil || parsed == nil || *parsed != scope {
		t.Fatal("claim bindings did not round trip")
	}
	if legacy, err := ParseFileEvidenceToken("mdt_legacy"); err != nil || legacy != nil {
		t.Fatal("legacy machine credential compatibility changed")
	}
	for _, token := range []string{FileEvidenceTokenPrefix, first + ".extra", FileEvidenceTokenPrefix + "bad.x", first[:strings.LastIndex(first, ".")] + ".00", FileEvidenceTokenPrefix + strings.Repeat("a", 2049)} {
		if _, err := ParseFileEvidenceToken(token); err == nil {
			t.Fatal("accepted malformed scoped credential")
		}
	}
	scope.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	if _, err := GenerateFileEvidenceToken(scope); err == nil {
		t.Fatal("minted expired credential")
	}
}
