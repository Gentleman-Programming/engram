package hostbind

import "testing"

func TestSameProcPIDNamespace(t *testing.T) {
	if !sameProcPIDNamespace("Name:\tfixture\nNSpid:\t123\n", "123") {
		t.Fatal("qualified proc namespace rejected")
	}
	for _, status := range []string{"Name:\tfixture\n", "NSpid:\t123\t123\n", "NSpid:\t456\n"} {
		if sameProcPIDNamespace(status, "123") {
			t.Fatalf("uncertain proc namespace accepted: %q", status)
		}
	}
}
