package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
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

func TestManualVerificationPersistsCurrentStateAndHistory(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := store.CreateHost(ctx, "test", "tcp://docker:2375", "token", db.Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upsnap-ready"))
	}))
	defer srv.Close()
	checks, _ := json.Marshal([]VerificationCheck{{Type: "http", URL: srv.URL, ExpectedStatus: http.StatusOK, ExpectedContent: "ready", TimeoutSeconds: 2}})
	if err := store.SaveVerificationProfile(ctx, db.VerificationProfile{HostID: hostID, ScopeType: "container", ScopeKey: "upsnap", Enabled: db.Bool(true), RetryCount: 0, RetryIntervalSeconds: 0, ChecksJSON: string(checks)}); err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store, Logger: slog.Default()}
	result := a.runCustomVerification(ctx, hostID, "upsnap", "manual-test", "owner", 0)
	if result.Status != verificationStatusVerified {
		t.Fatalf("manual verification should pass, got %#v", result)
	}
	state, err := store.VerificationState(ctx, hostID, "upsnap")
	if err != nil || state.Status != verificationStatusVerified || state.CheckedAt == "" {
		t.Fatalf("unexpected verification state: %#v err=%v", state, err)
	}
	history, err := store.VerificationHistory(ctx, hostID, "upsnap", 10)
	if err != nil || len(history) != 1 || history[0].Trigger != "manual-test" || history[0].Status != verificationStatusVerified {
		t.Fatalf("unexpected verification history: %#v err=%v", history, err)
	}

	// The effective profile must not appear newer than a verification that was
	// executed immediately after saving it. Verification timestamps therefore
	// retain sub-second precision.
	profile, err := a.effectiveVerificationProfile(ctx, hostID, "upsnap")
	if err != nil {
		t.Fatal(err)
	}
	if presented := verificationStateForProfile(profile, state); presented.Status != verificationStatusVerified {
		t.Fatalf("fresh verification must present as verified, got %#v (profile updated_at=%s state checked_at=%s)", presented, profile.UpdatedAt, state.CheckedAt)
	}

	// Saving the exact same profile again is not a configuration change and
	// must not invalidate an already successful verification result.
	before, err := store.VerificationProfile(ctx, hostID, "container", "upsnap")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveVerificationProfile(ctx, before); err != nil {
		t.Fatal(err)
	}
	after, err := store.VerificationProfile(ctx, hostID, "container", "upsnap")
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("identical profile save changed updated_at: before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
	}
	profile = verificationProfileView(after, false)
	if presented := verificationStateForProfile(profile, state); presented.Status != verificationStatusVerified {
		t.Fatalf("identical profile save must keep verification valid, got %#v", presented)
	}

	// A real profile change still invalidates the previous result.
	time.Sleep(2 * time.Millisecond)
	after.RetryCount++
	if err := store.SaveVerificationProfile(ctx, after); err != nil {
		t.Fatal(err)
	}
	changed, err := store.VerificationProfile(ctx, hostID, "container", "upsnap")
	if err != nil {
		t.Fatal(err)
	}
	if changed.UpdatedAt == after.UpdatedAt {
		t.Fatal("changed profile did not advance updated_at")
	}
	profile = verificationProfileView(changed, false)
	if presented := verificationStateForProfile(profile, state); presented.Status != "pending" {
		t.Fatalf("changed profile must invalidate old verification result, got %#v", presented)
	}
}

func TestStackVerificationStateIsSharedAcrossMembers(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	profile := VerificationProfileView{HostID: 1, ScopeType: "stack", ScopeKey: "immich", Enabled: true, Configured: true}
	a := &App{Store: store, Logger: slog.Default()}
	if err := a.saveVerificationStateForProfile(ctx, profile, 1, "immich_server", verificationStatusVerified, `[]`, "2026-08-13T20:20:00.123456789Z", ""); err != nil {
		t.Fatal(err)
	}
	server := a.effectiveVerificationState(ctx, 1, "immich_server", profile)
	postgres := a.effectiveVerificationState(ctx, 1, "immich_postgres", profile)
	if server.Status != verificationStatusVerified || postgres.Status != verificationStatusVerified {
		t.Fatalf("stack members must share verification state: server=%#v postgres=%#v", server, postgres)
	}
	if postgres.ScopeType != "stack" || postgres.ScopeKey != "immich" || postgres.ContainerName != "immich_postgres" {
		t.Fatalf("shared state must retain stack scope while projecting to member: %#v", postgres)
	}
	legacy, err := store.VerificationState(ctx, 1, "immich_server")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Status == verificationStatusVerified {
		t.Fatalf("stack verification must not be persisted as a container-only state: %#v", legacy)
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

func TestClassifyVersionChange(t *testing.T) {
	cases := []struct {
		old  string
		new  string
		want string
	}{
		{"1.2.3", "2.0.0", "major"},
		{"1.2.3", "1.3.0", "minor"},
		{"1.2.3", "1.2.4", "patch"},
		{"1.2.3", "1.2.3", "image"},
		{"latest", "latest", "unknown"},
	}
	for _, tc := range cases {
		if got := classifyVersionChange(tc.old, tc.new); got != tc.want {
			t.Fatalf("classifyVersionChange(%q,%q)=%q want %q", tc.old, tc.new, got, tc.want)
		}
	}
}

func TestExplicitSecurityReleaseRequiresExplicitMetadata(t *testing.T) {
	if !explicitSecurityRelease("Security release", "Fixes CVE-2026-1234") {
		t.Fatal("explicit security release should be detected")
	}
	if !explicitSecurityRelease("Bugfix release", "Includes a security fix for authentication") {
		t.Fatal("explicit security wording should be detected")
	}
	if explicitSecurityRelease("Patch release", "Several bug fixes and performance improvements") {
		t.Fatal("ordinary patch release must not be labeled as security")
	}
}
