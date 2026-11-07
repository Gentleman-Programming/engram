package remote

import (
	"strings"
	"testing"
)

func TestChunkEvidenceScope(t *testing.T) {
	makeTransport := func(base, token, project string) *RemoteTransport {
		t.Helper()
		tr, err := NewRemoteTransport(base, token, project)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	a := makeTransport("https://synthetic.invalid/", "private-token-a", "Audit")
	b := makeTransport("https://synthetic.invalid", "private-token-b", "audit")
	if a.ChunkEvidenceScope() != b.ChunkEvidenceScope() {
		t.Fatal("normalized source changed when rotating credentials")
	}
	for _, other := range []*RemoteTransport{
		makeTransport("https://other.invalid", "private-token-a", "audit"),
		makeTransport("https://synthetic.invalid", "private-token-a", "other"),
	} {
		if a.ChunkEvidenceScope() == other.ChunkEvidenceScope() {
			t.Fatal("distinct source/project shared evidence scope")
		}
	}
	if strings.Contains(a.ChunkEvidenceScope(), "private-token") {
		t.Fatal("scope contains a bearer token")
	}
}
