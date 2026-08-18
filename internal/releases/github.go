package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type GitHubClient struct {
	HTTP  *http.Client
	Token string
	mu    sync.Mutex
	cache map[string]cachedRelease
}
type cachedRelease struct {
	Release Release
	At      time.Time
}
type Release struct {
	Repository  string `json:"repository"`
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
	Source      string `json:"source"`
}

type ghRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
}

func New() *GitHubClient {
	return &GitHubClient{HTTP: &http.Client{Timeout: 15 * time.Second}, Token: os.Getenv("GITHUB_TOKEN"), cache: map[string]cachedRelease{}}
}

var ghRE = regexp.MustCompile(`(?i)(?:https?://)?github\.com/([^/]+)/([^/#]+)`)

func normalizeRepo(v string) (string, bool) {
	v = strings.TrimSpace(strings.TrimSuffix(v, ".git"))
	if v == "" {
		return "", false
	}
	if strings.Count(v, "/") == 1 && !strings.Contains(v, "://") {
		return strings.Trim(v, "/"), true
	}
	m := ghRE.FindStringSubmatch(v)
	if len(m) == 3 {
		return m[1] + "/" + strings.TrimSuffix(m[2], ".git"), true
	}
	return "", false
}

func DetectFromLabels(labels map[string]string) (string, bool) {
	for _, k := range []string{"org.opencontainers.image.source", "org.label-schema.vcs-url", "maintainer.source"} {
		if r, ok := normalizeRepo(labels[k]); ok {
			return r, true
		}
	}
	return "", false
}

func FallbackFromImage(image string) (string, bool) {
	base := strings.Split(image, "@")[0]
	mappings := map[string]string{
		"ghcr.io/immich-app/immich-server":      "immich-app/immich",
		"ghcr.io/paperless-ngx/paperless-ngx":   "paperless-ngx/paperless-ngx",
		"ghcr.io/home-assistant/home-assistant": "home-assistant/core",
		"vaultwarden/server":                    "dani-garcia/vaultwarden",
		"netbirdio/netbird":                     "netbirdio/netbird",
		"ghcr.io/dani-garcia/vaultwarden":       "dani-garcia/vaultwarden",
	}
	for prefix, repo := range mappings {
		if strings.HasPrefix(base, prefix+":") || base == prefix {
			return repo, true
		}
	}
	return "", false
}

func (g *GitHubClient) Latest(ctx context.Context, repo string) (Release, error) {
	repo, ok := normalizeRepo(repo)
	if !ok {
		return Release{}, fmt.Errorf("invalid GitHub repository")
	}
	g.mu.Lock()
	if hit, ok := g.cache[repo]; ok && time.Since(hit.At) < 30*time.Minute {
		g.mu.Unlock()
		return hit.Release, nil
	}
	g.mu.Unlock()
	parts := strings.Split(repo, "/")
	u := "https://api.github.com/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "vibewatch")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("GitHub HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var x ghRelease
	if err := json.Unmarshal(body, &x); err != nil {
		return Release{}, err
	}
	rel := Release{Repository: repo, TagName: x.TagName, Name: x.Name, Body: x.Body, PublishedAt: x.PublishedAt, HTMLURL: x.HTMLURL, Source: "github"}
	g.mu.Lock()
	g.cache[repo] = cachedRelease{Release: rel, At: time.Now()}
	g.mu.Unlock()
	return rel, nil
}
