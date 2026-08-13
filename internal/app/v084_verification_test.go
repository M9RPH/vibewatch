package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func TestVerificationHTTPStatusAndContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("service-ready"))
	}))
	defer srv.Close()
	a := &App{}
	got := a.executeVerificationCheck(context.Background(), VerificationCheck{Type: "http", URL: srv.URL, ExpectedStatus: 200, ExpectedContent: "ready", TimeoutSeconds: 2})
	if got.Status != verificationStatusVerified || got.HTTPStatus != 200 || got.Error != "" {
		t.Fatalf("expected verified HTTP check, got %#v", got)
	}
	bad := a.executeVerificationCheck(context.Background(), VerificationCheck{Type: "http", URL: srv.URL, ExpectedStatus: 204, TimeoutSeconds: 2})
	if bad.Status != verificationStatusFailed || !strings.Contains(bad.Error, "expected HTTP 204") {
		t.Fatalf("expected status mismatch, got %#v", bad)
	}
}

func TestVerificationTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	a := &App{}
	got := a.executeVerificationCheck(context.Background(), VerificationCheck{Type: "tcp", Host: "127.0.0.1", Port: addr.Port, TimeoutSeconds: 2})
	if got.Status != verificationStatusVerified {
		t.Fatalf("expected verified TCP check, got %#v", got)
	}
}

func TestNormalizeVerificationProfile(t *testing.T) {
	p, err := normalizeVerificationProfile(VerificationProfileView{ScopeType: "container", ScopeKey: "app", Enabled: true, RetryCount: 2, RetryIntervalSeconds: 3, Checks: []VerificationCheck{{Type: "https", URL: "https://example.invalid/health"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Checks[0].ExpectedStatus != 200 || p.Checks[0].TimeoutSeconds != 8 {
		t.Fatalf("defaults missing: %#v", p.Checks[0])
	}
	if _, err := normalizeVerificationProfile(VerificationProfileView{ScopeType: "container", ScopeKey: "app", Enabled: true, Checks: []VerificationCheck{{Type: "tcp", Host: "", Port: 0}}}); err == nil {
		t.Fatal("expected invalid TCP profile to fail")
	}
}

func TestMajorPart(t *testing.T) {
	cases := map[string]int{"v3.4.1": 3, "17": 17, "2-beta": 2}
	for in, want := range cases {
		got, ok := majorPart(in)
		if !ok || got != want {
			t.Fatalf("majorPart(%q)=%d,%v want %d,true", in, got, ok, want)
		}
	}
	if _, ok := majorPart("latest"); ok {
		t.Fatal("latest must not be parsed as a major version")
	}
}

func TestVerificationStateTracksEffectiveProfileFreshness(t *testing.T) {
	profile := VerificationProfileView{Configured: false}
	state := db.VerificationState{Status: verificationStatusVerified, CheckedAt: "2026-08-12T20:00:00Z"}
	if got := verificationStateForProfile(profile, state); got.Status != verificationStatusNotConfigured {
		t.Fatalf("disabled/missing profile must present not configured, got %#v", got)
	}
	profile = VerificationProfileView{Configured: true, UpdatedAt: "2026-08-12T20:10:00Z"}
	if got := verificationStateForProfile(profile, state); got.Status != "pending" {
		t.Fatalf("newer profile must invalidate old verification result, got %#v", got)
	}
	profile.UpdatedAt = "2026-08-12T19:00:00Z"
	if got := verificationStateForProfile(profile, state); got.Status != verificationStatusVerified {
		t.Fatalf("fresh verification should remain verified, got %#v", got)
	}
	if _, err := time.Parse(time.RFC3339Nano, profile.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}
