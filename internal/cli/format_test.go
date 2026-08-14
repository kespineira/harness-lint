package cli

import "testing"

func TestFormatAdvertisedSessionEvidence(t *testing.T) {
	invoked, advertised := int64(0), int64(1)
	if got := formatAdvertisedSessionEvidence(&invoked, &advertised); got != "invoked in 0 / 1 advertised sessions" {
		t.Fatalf("known advertised-session evidence = %q, want exact contract string", got)
	}
	if got := formatAdvertisedSessionEvidence(nil, nil); got != "unknown" {
		t.Fatalf("unknown advertised-session evidence = %q, want unknown", got)
	}
}
