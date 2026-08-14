package app

import (
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func TestV093AutomationHoldVisibility(t *testing.T) {
	cache := db.Cache{UpdateAvailable: db.Bool(true), LatestDigest: "sha256:new"}
	policy := db.Policy{Mode: "auto"}
	history := []db.UpdateHistory{{Status: "skipped", Trigger: "automation:7", PreflightStatus: "blocked", Error: "Automatic update held by Preflight", TS: "2026-08-13T22:00:00Z"}}

	hold := deriveAutomationHold(cache, policy, nil, history)
	if hold == nil || !hold.Held || hold.Source != "container" {
		t.Fatalf("expected individual automatic hold, got %#v", hold)
	}

	chain := &ChainManagementView{ChainID: 4, ChainName: "immich", PolicyMode: "auto", LastStatus: "blocked", LastRunAt: "2026-08-13T22:10:00Z"}
	hold = deriveAutomationHold(cache, policy, chain, nil)
	if hold == nil || hold.Source != "chain" || hold.ChainID != 4 {
		t.Fatalf("expected chain hold, got %#v", hold)
	}

	cache.SnoozedDigest = cache.LatestDigest
	if hold := deriveAutomationHold(cache, policy, chain, history); hold != nil {
		t.Fatalf("snoozed update must not remain in held attention state: %#v", hold)
	}

	cache.SnoozedDigest = ""
	cache.UpdateAvailable = db.Bool(false)
	if hold := deriveAutomationHold(cache, policy, chain, history); hold != nil {
		t.Fatalf("current container must not remain held: %#v", hold)
	}
}
