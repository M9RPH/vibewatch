package app

import "testing"

func TestServiceIconCandidatesPreferImageAndAliases(t *testing.T) {
	got := serviceIconCandidates("plexx", "plexx", "plex", "lscr.io/linuxserver/plex:latest")
	if len(got) == 0 {
		t.Fatal("expected candidates")
	}
	if got[0].Slug != "plex" || got[0].Source != "image" {
		t.Fatalf("expected image-derived plex first, got %#v", got[0])
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.Slug] {
			t.Fatalf("duplicate slug %q in %#v", c.Slug, got)
		}
		seen[c.Slug] = true
	}
}

func TestServiceIconCandidatesNormalizeComposeAndPostgres(t *testing.T) {
	got := serviceIconCandidates("paperless-db-1", "paperless", "db", "postgres:17-alpine")
	if len(got) == 0 || got[0].Slug != "postgresql" {
		t.Fatalf("expected postgresql image candidate first, got %#v", got)
	}
}

func TestServiceIconFallback(t *testing.T) {
	category, initials := serviceIconFallback("custom-postgres", "", "database", "postgres:17")
	if category != "database" {
		t.Fatalf("expected database fallback, got %q", category)
	}
	if initials != "DA" {
		t.Fatalf("expected DA initials, got %q", initials)
	}
}

func TestSafeServiceIconSlug(t *testing.T) {
	good := []string{"plex", "nginx-proxy-manager", "2fauth"}
	for _, v := range good {
		if safeServiceIconSlug(v) != v {
			t.Fatalf("expected %q to be accepted", v)
		}
	}
	if safeServiceIconSlug("Plex") != "plex" {
		t.Fatal("mixed-case slug should normalize to lowercase")
	}
	bad := []string{"../plex", "plex.svg", "", "a/b"}
	for _, v := range bad {
		if safeServiceIconSlug(v) != "" {
			t.Fatalf("expected %q to be rejected", v)
		}
	}
}

func TestCatalogScore(t *testing.T) {
	if catalogScore("plex", "plex") <= catalogScore("plex-meta-manager", "plex") {
		t.Fatal("exact match should outrank prefix match")
	}
	if catalogScore("nginx-proxy-manager", "proxy") == 0 {
		t.Fatal("word-prefix match should score")
	}
}
