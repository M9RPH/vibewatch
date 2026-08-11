package releases

import "testing"

func TestNetBirdFallback(t *testing.T) {
	r, ok := FallbackFromImage("netbirdio/netbird:latest")
	if !ok || r != "netbirdio/netbird" {
		t.Fatalf("got %q %v", r, ok)
	}
}
