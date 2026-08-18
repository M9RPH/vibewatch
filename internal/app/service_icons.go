package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dashboardIconsBaseURL     = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons"
	dashboardIconsCatalogTTL  = 24 * time.Hour
	dashboardIconsNegativeTTL = 6 * time.Hour
	dashboardIconMaxBytes     = 2 << 20
)

var serviceIconSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,95}$`)

type serviceIconCandidate struct {
	Slug       string `json:"slug"`
	Confidence int    `json:"confidence"`
	Source     string `json:"source"`
}

type serviceIconResolveView struct {
	Candidates   []serviceIconCandidate `json:"candidates"`
	OverrideSlug string                 `json:"override_slug,omitempty"`
	Category     string                 `json:"category"`
	Initials     string                 `json:"initials"`
}

type serviceIconCatalogItem struct {
	Slug string `json:"slug"`
}

func (a *App) serviceIconCacheDir() string {
	return filepath.Join(a.Cfg.DataDir, "icon-cache", "dashboard-icons")
}

func serviceIconOverrideKey(hostID int64, container string) string {
	return fmt.Sprintf("service_icon_override:%d:%s", hostID, strings.TrimSpace(container))
}

func safeServiceIconSlug(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if !serviceIconSlugRE.MatchString(v) {
		return ""
	}
	return v
}

func normalizeServiceIconName(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, "_", "-")
	v = strings.ReplaceAll(v, ".", "-")
	v = strings.ReplaceAll(v, " ", "-")
	var b strings.Builder
	dash := false
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	v = strings.Trim(b.String(), "-")
	// Docker Compose often adds a numeric replica suffix to the container name.
	parts := strings.Split(v, "-")
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			v = strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return v
}

func serviceIconImageName(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if i := strings.LastIndex(image, ":"); i >= 0 {
		image = image[:i]
	}
	return normalizeServiceIconName(image)
}

var serviceIconAliases = map[string]string{
	"adguardhome":             "adguard-home",
	"adguard-home":            "adguard-home",
	"homeassistant":           "home-assistant",
	"home-assistant":          "home-assistant",
	"immich-server":           "immich",
	"immich-machine-learning": "immich",
	"immich-microservices":    "immich",
	"immich":                  "immich",
	"linuxserver-plex":        "plex",
	"plexx":                   "plex",
	"plex":                    "plex",
	"paperless":               "paperless-ngx",
	"paperless-ngx":           "paperless-ngx",
	"postgres":                "postgresql",
	"postgresql":              "postgresql",
	"qbit":                    "qbittorrent",
	"qbittorrent":             "qbittorrent",
	"nginxproxymanager":       "nginx-proxy-manager",
	"nginx-proxy-manager":     "nginx-proxy-manager",
	"vaultwarden-server":      "vaultwarden",
	"vaultwarden":             "vaultwarden",
}

func canonicalServiceIconSlug(v string) string {
	v = normalizeServiceIconName(v)
	if alias := serviceIconAliases[v]; alias != "" {
		return alias
	}
	return v
}

func serviceIconCandidates(container, stack, service, image string) []serviceIconCandidate {
	type input struct {
		value      string
		confidence int
		source     string
	}
	inputs := []input{
		{serviceIconImageName(image), 98, "image"},
		{service, 94, "compose service"},
		{stack, 86, "stack"},
		{container, 82, "container"},
	}
	seen := map[string]bool{}
	out := make([]serviceIconCandidate, 0, 8)
	add := func(slug string, confidence int, source string) {
		slug = canonicalServiceIconSlug(slug)
		if slug == "" || !serviceIconSlugRE.MatchString(slug) || seen[slug] {
			return
		}
		seen[slug] = true
		out = append(out, serviceIconCandidate{Slug: slug, Confidence: confidence, Source: source})
	}
	for _, in := range inputs {
		raw := normalizeServiceIconName(in.value)
		add(raw, in.confidence, in.source)
		// Common role suffixes are tried only as a lower-confidence alternative.
		for _, suffix := range []string{"-server", "-service", "-container", "-app", "-web", "-webserver"} {
			if strings.HasSuffix(raw, suffix) && len(raw) > len(suffix)+1 {
				add(strings.TrimSuffix(raw, suffix), in.confidence-8, in.source+" base")
			}
		}
	}
	return out
}

func serviceIconFallback(container, stack, service, image string) (category, initials string) {
	joined := strings.ToLower(strings.Join([]string{container, stack, service, image}, " "))
	categories := []struct {
		name   string
		tokens []string
	}{
		{"database", []string{"postgres", "mariadb", "mysql", "mongo", "redis", "database"}},
		{"media", []string{"plex", "jellyfin", "emby", "sonarr", "radarr", "bazarr", "prowlarr", "immich", "media", "photo"}},
		{"download", []string{"sabnzbd", "qbittorrent", "torrent", "download", "nzb", "transmission"}},
		{"network", []string{"gluetun", "wireguard", "tailscale", "netbird", "proxy", "nginx", "traefik", "caddy", "vpn", "dns"}},
		{"monitoring", []string{"grafana", "prometheus", "netdata", "monitor", "uptime", "metrics"}},
		{"storage", []string{"minio", "syncthing", "storage", "backup", "archive"}},
		{"security", []string{"vaultwarden", "bitwarden", "authentik", "authelia", "security", "auth"}},
		{"web", []string{"apache", "httpd", "nginx", "web", "frontend"}},
	}
	category = "container"
	for _, exact := range []string{normalizeServiceIconName(service), normalizeServiceIconName(container)} {
		if exact == "db" || exact == "database" {
			category = "database"
			break
		}
	}
	for _, c := range categories {
		if category != "container" {
			break
		}
		for _, token := range c.tokens {
			if strings.Contains(joined, token) {
				category = c.name
				break
			}
		}
		if category == c.name {
			break
		}
	}
	base := strings.TrimSpace(service)
	if base == "" {
		base = strings.TrimSpace(container)
	}
	words := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' })
	if len(words) >= 2 {
		initials = strings.ToUpper(string([]rune(words[0])[0]) + string([]rune(words[1])[0]))
	} else if len(words) == 1 {
		rr := []rune(words[0])
		if len(rr) >= 2 {
			initials = strings.ToUpper(string(rr[:2]))
		} else if len(rr) == 1 {
			initials = strings.ToUpper(string(rr[0]))
		}
	}
	if initials == "" {
		initials = "CT"
	}
	return category, initials
}

func (a *App) handleServiceIconResolve(w http.ResponseWriter, r *http.Request) {
	hostID, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	if hostID <= 0 || container == "" {
		writeErr(w, http.StatusBadRequest, "host_id and container are required")
		return
	}
	if !a.hostAllowed(r, hostID) {
		writeErr(w, http.StatusForbidden, "host access denied")
		return
	}
	override := safeServiceIconSlug(a.Store.Setting(r.Context(), serviceIconOverrideKey(hostID, container), ""))
	category, initials := serviceIconFallback(container, r.URL.Query().Get("stack"), r.URL.Query().Get("service"), r.URL.Query().Get("image"))
	candidates := serviceIconCandidates(container, r.URL.Query().Get("stack"), r.URL.Query().Get("service"), r.URL.Query().Get("image"))
	if override != "" {
		candidates = append([]serviceIconCandidate{{Slug: override, Confidence: 100, Source: "manual override"}}, candidates...)
	}
	// De-duplicate again when an override matches automatic detection.
	seen := map[string]bool{}
	dedup := candidates[:0]
	for _, c := range candidates {
		if seen[c.Slug] {
			continue
		}
		seen[c.Slug] = true
		dedup = append(dedup, c)
	}
	writeJSON(w, http.StatusOK, serviceIconResolveView{Candidates: dedup, OverrideSlug: override, Category: category, Initials: initials})
}

func (a *App) dashboardIconPath(slug string) string {
	return filepath.Join(a.serviceIconCacheDir(), slug+".png")
}

func (a *App) dashboardIconMissingPath(slug string) string {
	return filepath.Join(a.serviceIconCacheDir(), slug+".missing")
}

func freshFile(path string, ttl time.Duration) bool {
	st, err := os.Stat(path)
	return err == nil && time.Since(st.ModTime()) < ttl
}

func (a *App) ensureDashboardIcon(ctx context.Context, slug string) (string, error) {
	slug = safeServiceIconSlug(slug)
	if slug == "" {
		return "", errors.New("invalid icon slug")
	}
	if err := os.MkdirAll(a.serviceIconCacheDir(), 0o750); err != nil {
		return "", err
	}
	path := a.dashboardIconPath(slug)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path, nil
	}
	missing := a.dashboardIconMissingPath(slug)
	if freshFile(missing, dashboardIconsNegativeTTL) {
		return "", os.ErrNotExist
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dashboardIconsBaseURL+"/png/"+slug+".png", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Vibewatch/"+a.Cfg.Version+" service-icon-cache")
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_ = os.WriteFile(missing, []byte(time.Now().UTC().Format(time.RFC3339)), 0o640)
		return "", os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dashboard icons returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dashboardIconMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) < 8 || len(body) > dashboardIconMaxBytes || string(body[:8]) != "\x89PNG\r\n\x1a\n" {
		return "", errors.New("invalid dashboard icon payload")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o640); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_ = os.Remove(missing)
	return path, nil
}

func (a *App) handleServiceIconFile(w http.ResponseWriter, r *http.Request) {
	slug := safeServiceIconSlug(r.PathValue("slug"))
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	path, err := a.ensureDashboardIcon(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, path)
}

func (a *App) handleServiceIconOverride(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	var in struct {
		HostID    int64  `json:"host_id"`
		Container string `json:"container"`
		Slug      string `json:"slug"`
	}
	if r.Method == http.MethodDelete {
		in.HostID, _ = strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
		in.Container = strings.TrimSpace(r.URL.Query().Get("container"))
		if in.HostID <= 0 || in.Container == "" {
			writeErr(w, http.StatusBadRequest, "host_id and container are required")
			return
		}
		if err := a.Store.SetSetting(r.Context(), serviceIconOverrideKey(in.HostID, in.Container), ""); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Container = strings.TrimSpace(in.Container)
	in.Slug = safeServiceIconSlug(in.Slug)
	if in.HostID <= 0 || in.Container == "" || in.Slug == "" {
		writeErr(w, http.StatusBadRequest, "host_id, container and valid slug are required")
		return
	}
	// Validate and warm the cache before persisting an override.
	if _, err := a.ensureDashboardIcon(r.Context(), in.Slug); err != nil {
		writeErr(w, http.StatusBadRequest, "Dashboard Icons slug not found")
		return
	}
	if err := a.Store.SetSetting(r.Context(), serviceIconOverrideKey(in.HostID, in.Container), in.Slug); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "container.icon", in.HostID, in.Container, "dashboard-icons:"+in.Slug)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": in.Slug})
}

func (a *App) dashboardIconCatalog(ctx context.Context) ([]string, error) {
	if err := os.MkdirAll(a.serviceIconCacheDir(), 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(a.serviceIconCacheDir(), "tree.json")
	load := func() ([]string, error) {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var tree map[string][]string
		if err := json.Unmarshal(body, &tree); err != nil {
			return nil, err
		}
		files := tree["png"]
		slugs := make([]string, 0, len(files))
		seen := map[string]bool{}
		for _, name := range files {
			if !strings.HasSuffix(name, ".png") {
				continue
			}
			slug := safeServiceIconSlug(strings.TrimSuffix(filepath.Base(name), ".png"))
			if slug == "" || seen[slug] {
				continue
			}
			seen[slug] = true
			slugs = append(slugs, slug)
		}
		return slugs, nil
	}
	if freshFile(path, dashboardIconsCatalogTTL) {
		return load()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dashboardIconsBaseURL+"/tree.json", nil)
	if err == nil {
		req.Header.Set("User-Agent", "Vibewatch/"+a.Cfg.Version+" service-icon-catalog")
		resp, fetchErr := (&http.Client{Timeout: 8 * time.Second}).Do(req)
		if fetchErr == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
				if readErr == nil && len(body) > 0 {
					var probe map[string][]string
					if json.Unmarshal(body, &probe) == nil && len(probe["png"]) > 0 {
						tmp := path + ".tmp"
						if os.WriteFile(tmp, body, 0o640) == nil {
							_ = os.Rename(tmp, path)
						}
					}
				}
			}
		}
	}
	// A stale local catalogue is better than losing manual icon search offline.
	return load()
}

func catalogScore(slug, q string) int {
	if slug == q {
		return 100
	}
	if strings.HasPrefix(slug, q) {
		return 90
	}
	for _, part := range strings.Split(slug, "-") {
		if strings.HasPrefix(part, q) {
			return 82
		}
	}
	if strings.Contains(slug, q) {
		return 70
	}
	return 0
}

func (a *App) handleServiceIconCatalog(w http.ResponseWriter, r *http.Request) {
	q := normalizeServiceIconName(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, []serviceIconCatalogItem{})
		return
	}
	slugs, err := a.dashboardIconCatalog(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Dashboard Icons catalog unavailable")
		return
	}
	type scored struct {
		slug  string
		score int
	}
	matches := make([]scored, 0, 32)
	for _, slug := range slugs {
		score := catalogScore(slug, q)
		if score > 0 {
			matches = append(matches, scored{slug: slug, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if len(matches[i].slug) != len(matches[j].slug) {
			return len(matches[i].slug) < len(matches[j].slug)
		}
		return matches[i].slug < matches[j].slug
	})
	if len(matches) > 30 {
		matches = matches[:30]
	}
	out := make([]serviceIconCatalogItem, 0, len(matches))
	for _, m := range matches {
		out = append(out, serviceIconCatalogItem{Slug: m.slug})
	}
	writeJSON(w, http.StatusOK, out)
}
