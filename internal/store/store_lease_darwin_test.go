//go:build darwin

package store

import "testing"

func TestStoreLeasePathCanonicalizesTmpAliases(t *testing.T) {
	fromTmp, err := storeLeasePath("/tmp/engram-lease-alias-test")
	if err != nil {
		t.Fatalf("derive /tmp lease path: %v", err)
	}
	fromPrivateTmp, err := storeLeasePath("/private/tmp/engram-lease-alias-test")
	if err != nil {
		t.Fatalf("derive /private/tmp lease path: %v", err)
	}
	if fromTmp != fromPrivateTmp {
		t.Fatalf("temporary-directory aliases yield %q and %q, want one lease identity", fromTmp, fromPrivateTmp)
	}
}
