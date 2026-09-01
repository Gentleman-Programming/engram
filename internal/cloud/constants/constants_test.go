package constants

import "testing"

func TestMutationPayloadInvalidErrorCodeIsWireSpecific(t *testing.T) {
	if MutationErrorCodePayloadInvalid != "payload_invalid" {
		t.Fatalf("mutation payload error code: got %q, want %q", MutationErrorCodePayloadInvalid, "payload_invalid")
	}
	if UpgradeErrorCodePayloadInvalid != "upgrade_repairable_payload_invalid" {
		t.Fatalf("chunk upgrade payload error code changed: got %q, want %q", UpgradeErrorCodePayloadInvalid, "upgrade_repairable_payload_invalid")
	}
	if MutationErrorCodePayloadInvalid == UpgradeErrorCodePayloadInvalid {
		t.Fatalf("mutation payload error code must remain distinct from chunk upgrade code %q", UpgradeErrorCodePayloadInvalid)
	}
}

func TestMutationWirePayloadInvalidCodeIsStable(t *testing.T) {
	if got := MutationErrorCodePayloadInvalid; got != "payload_invalid" {
		t.Fatalf("mutation wire code: got %q, want %q", got, "payload_invalid")
	}
	if got := UpgradeErrorCodePayloadInvalid; got != "upgrade_repairable_payload_invalid" {
		t.Fatalf("chunk upgrade compatibility code: got %q, want %q", got, "upgrade_repairable_payload_invalid")
	}
}
