package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type ImageRef struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// Platform identifies the target image platform. Architecture aliases reported
// by Docker/hosts (for example x86_64/aarch64) are normalized before matching
// registry manifest-list descriptors.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type RemoteImageState struct {
	ManifestDigest string   `json:"manifest_digest"`
	ConfigDigest   string   `json:"config_digest"`
	Platform       Platform `json:"platform"`
}

type Version struct {
	Version    string `json:"version"`
	Source     string `json:"source"`
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
}

type cached struct {
	V  Version
	At time.Time
}

type Credential struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"-"`
}

type Client struct {
	HTTP  *http.Client
	mu    sync.Mutex
	cache map[string]cached
	creds map[string]Credential
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}, cache: map[string]cached{}, creds: map[string]Credential{}}
}

func normalizeRegistryHost(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimSuffix(v, "/")
	if v == "docker.io" || v == "index.docker.io" {
		return "registry-1.docker.io"
	}
	return v
}

func (c *Client) SetCredentials(xs []Credential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creds = map[string]Credential{}
	for _, x := range xs {
		reg := normalizeRegistryHost(x.Registry)
		if reg == "" || strings.TrimSpace(x.Secret) == "" {
			continue
		}
		x.Registry = reg
		c.creds[reg] = x
	}
	// Authentication can change the readable manifest/version, so never retain
	// metadata cached under a previous credential set.
	c.cache = map[string]cached{}
}

func (c *Client) credential(registry string) Credential {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creds[normalizeRegistryHost(registry)]
}

func Parse(image string) (ImageRef, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return ImageRef{}, fmt.Errorf("empty image reference")
	}
	tag := "latest"
	digestRef := false
	if at := strings.Index(image, "@"); at >= 0 {
		if digest := strings.TrimSpace(image[at+1:]); digest != "" {
			tag = digest
			digestRef = true
		}
		image = image[:at]
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		if !digestRef {
			tag = image[lastColon+1:]
		}
		image = image[:lastColon]
	}
	parts := strings.Split(image, "/")
	reg := "registry-1.docker.io"
	repo := image
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		reg = parts[0]
		repo = strings.Join(parts[1:], "/")
	} else if !strings.Contains(image, "/") {
		repo = "library/" + image
	}
	if reg == "docker.io" || reg == "index.docker.io" {
		reg = "registry-1.docker.io"
	}
	if repo == "" || tag == "" {
		return ImageRef{}, fmt.Errorf("invalid image reference")
	}
	return ImageRef{Registry: reg, Repository: repo, Tag: tag}, nil
}

func normalizeArchitecture(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "x86_64", "x86-64", "x64":
		return "amd64"
	case "aarch64", "arm64v8", "arm64/v8":
		return "arm64"
	case "armhf", "armv7", "armv7l", "arm32v7":
		return "arm"
	case "i386", "i686", "x86":
		return "386"
	default:
		return v
	}
}

func normalizePlatform(p Platform) Platform {
	p.OS = strings.ToLower(strings.TrimSpace(p.OS))
	rawArch := strings.ToLower(strings.TrimSpace(p.Architecture))
	p.Architecture = normalizeArchitecture(rawArch)
	p.Variant = strings.ToLower(strings.TrimSpace(p.Variant))
	if p.Variant == "" {
		switch rawArch {
		case "armv7", "armv7l", "arm32v7":
			p.Variant = "v7"
		case "arm64v8", "arm64/v8":
			p.Variant = "v8"
		}
	}
	return p
}

func platformKey(p Platform) string {
	p = normalizePlatform(p)
	return p.OS + "/" + p.Architecture + "/" + p.Variant
}

func platformLabel(p Platform) string {
	p = normalizePlatform(p)
	out := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		out += "/" + p.Variant
	}
	return strings.Trim(out, "/")
}

func choosePlatformManifest(manifests any, wanted Platform) (string, Platform, error) {
	bs, _ := json.Marshal(manifests)
	var ms []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
		} `json:"platform"`
	}
	if err := json.Unmarshal(bs, &ms); err != nil {
		return "", Platform{}, fmt.Errorf("decode registry manifest list: %w", err)
	}
	wanted = normalizePlatform(wanted)
	if wanted.OS == "" {
		wanted.OS = runtime.GOOS
	}
	if wanted.Architecture == "" {
		wanted.Architecture = normalizeArchitecture(runtime.GOARCH)
	}

	// Prefer an exact variant match. When the caller has no variant, prefer a
	// descriptor without one and only then any descriptor for the same OS/arch.
	for _, exactVariant := range []bool{true, false} {
		for _, m := range ms {
			p := normalizePlatform(Platform{OS: m.Platform.OS, Architecture: m.Platform.Architecture, Variant: m.Platform.Variant})
			if p.OS != wanted.OS || p.Architecture != wanted.Architecture {
				continue
			}
			if exactVariant {
				if wanted.Variant != "" && p.Variant == wanted.Variant {
					return strings.TrimSpace(m.Digest), p, nil
				}
				if wanted.Variant == "" && p.Variant == "" {
					return strings.TrimSpace(m.Digest), p, nil
				}
				continue
			}
			if wanted.Variant == "" || p.Variant == wanted.Variant || p.Variant == "" {
				return strings.TrimSpace(m.Digest), p, nil
			}
		}
	}
	return "", Platform{}, fmt.Errorf("registry manifest list has no image for platform %s", platformLabel(wanted))
}

func manifestMediaType(m map[string]any) string {
	mt, _ := m["mediaType"].(string)
	return strings.ToLower(strings.TrimSpace(mt))
}

func isManifestIndex(m map[string]any) bool {
	mt := manifestMediaType(m)
	return strings.Contains(mt, "image.index") || strings.Contains(mt, "manifest.list") || m["manifests"] != nil
}

func (c *Client) getPlatformManifest(ctx context.Context, ref ImageRef, p Platform) (map[string]any, string, string, Platform, error) {
	manifest, digest, auth, err := c.getManifest(ctx, ref, "", "")
	if err != nil {
		return nil, "", auth, Platform{}, err
	}
	p = normalizePlatform(p)
	if !isManifestIndex(manifest) {
		return manifest, digest, auth, p, nil
	}
	childDigest, matched, err := choosePlatformManifest(manifest["manifests"], p)
	if err != nil {
		return nil, "", auth, Platform{}, err
	}
	manifest, digest, auth, err = c.getManifest(ctx, ref, childDigest, auth)
	if err != nil {
		return nil, "", auth, Platform{}, err
	}
	return manifest, digest, auth, matched, nil
}

// RemoteDigest returns the immutable digest of the registry reference without
// downloading image layers or configuration blobs. It is retained for callers
// that intentionally want the top-level tag/index digest.
func (c *Client) RemoteDigest(ctx context.Context, image string) (string, error) {
	ref, err := Parse(image)
	if err != nil {
		return "", err
	}
	_, digest, _, err := c.getManifest(ctx, ref, "", "")
	if err != nil {
		return "", err
	}
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return "", fmt.Errorf("registry did not return Docker-Content-Digest")
	}
	return digest, nil
}

// RemoteStateForPlatform resolves only registry manifests for the requested
// target platform and returns the platform-specific image config digest. The
// config digest can be compared directly with Docker's local image ID, so an
// amd64-only manifest-list change does not falsely flag an arm64 host (or vice
// versa). No image layers or config blobs are downloaded.
func (c *Client) RemoteStateForPlatform(ctx context.Context, image string, p Platform) (RemoteImageState, error) {
	ref, err := Parse(image)
	if err != nil {
		return RemoteImageState{}, err
	}
	manifest, digest, _, matched, err := c.getPlatformManifest(ctx, ref, p)
	if err != nil {
		return RemoteImageState{}, err
	}
	cfg, ok := manifest["config"].(map[string]any)
	if !ok {
		return RemoteImageState{}, fmt.Errorf("registry manifest has no config")
	}
	configDigest, _ := cfg["digest"].(string)
	configDigest = strings.TrimSpace(configDigest)
	if configDigest == "" {
		return RemoteImageState{}, fmt.Errorf("registry manifest config digest missing")
	}
	return RemoteImageState{ManifestDigest: strings.TrimSpace(digest), ConfigDigest: configDigest, Platform: matched}, nil
}

func (c *Client) RemoteVersion(ctx context.Context, image string) (Version, error) {
	return c.RemoteVersionForPlatform(ctx, image, Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH})
}

// RemoteVersionForPlatform reads human-readable OCI version labels from the
// same platform-specific image that the target Docker host would run.
func (c *Client) RemoteVersionForPlatform(ctx context.Context, image string, p Platform) (Version, error) {
	ref, err := Parse(image)
	if err != nil {
		return Version{}, err
	}
	p = normalizePlatform(p)
	if p.OS == "" {
		p.OS = runtime.GOOS
	}
	if p.Architecture == "" {
		p.Architecture = normalizeArchitecture(runtime.GOARCH)
	}
	key := ref.Registry + "/" + ref.Repository + ":" + ref.Tag + "@" + platformKey(p)
	c.mu.Lock()
	if x, ok := c.cache[key]; ok && time.Since(x.At) < 30*time.Minute {
		c.mu.Unlock()
		return x.V, nil
	}
	c.mu.Unlock()
	manifest, digest, auth, _, err := c.getPlatformManifest(ctx, ref, p)
	if err != nil {
		return Version{}, err
	}
	cfg, ok := manifest["config"].(map[string]any)
	if !ok {
		return Version{}, fmt.Errorf("registry manifest has no config")
	}
	cd, _ := cfg["digest"].(string)
	if cd == "" {
		return Version{}, fmt.Errorf("registry manifest config digest missing")
	}
	labels, err := c.getConfigLabels(ctx, ref, cd, auth)
	if err != nil {
		return Version{}, err
	}
	version := ""
	for _, k := range []string{"org.opencontainers.image.version", "org.label-schema.version", "version", "VERSION"} {
		if v := strings.TrimSpace(labels[k]); v != "" {
			version = strings.TrimPrefix(v, "v")
			break
		}
	}
	source := ref.Registry
	if ref.Registry == "registry-1.docker.io" {
		source = "docker-hub"
	}
	if ref.Registry == "ghcr.io" {
		source = "ghcr"
	}
	out := Version{Version: version, Source: source, Registry: ref.Registry, Repository: ref.Repository, Tag: ref.Tag, Digest: digest}
	c.mu.Lock()
	c.cache[key] = cached{V: out, At: time.Now()}
	c.mu.Unlock()
	if version == "" {
		low := strings.ToLower(ref.Tag)
		if ref.Tag != "" && !strings.Contains(ref.Tag, ":") && low != "latest" && low != "stable" && low != "release" && low != "main" && low != "master" && low != "edge" && low != "nightly" {
			out.Version = strings.TrimPrefix(ref.Tag, "v")
			c.mu.Lock()
			c.cache[key] = cached{V: out, At: time.Now()}
			c.mu.Unlock()
			return out, nil
		}
		return out, fmt.Errorf("remote image has no readable version label")
	}
	return out, nil
}

var bearerRE = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func parseChallenge(v string) (string, map[string]string) {
	m := bearerRE.FindStringSubmatch(strings.TrimSpace(v))
	if len(m) != 2 {
		return "", nil
	}
	out := map[string]string{}
	// values we need do not normally contain unescaped commas
	for _, p := range strings.Split(m[1], ",") {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 {
			out[strings.ToLower(kv[0])] = strings.Trim(kv[1], `"`)
		}
	}
	return "bearer", out
}
func (c *Client) token(ctx context.Context, ch map[string]string, cred Credential) (string, error) {
	realm := ch["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry auth challenge missing realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if v := ch["service"]; v != "" {
		q.Set("service", v)
	}
	if v := ch["scope"]; v != "" {
		q.Set("scope", v)
	}
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "vibewatch")
	if strings.TrimSpace(cred.Secret) != "" {
		req.SetBasicAuth(cred.Username, cred.Secret)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("registry token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var x struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &x); err != nil {
		return "", err
	}
	if x.Token == "" {
		x.Token = x.AccessToken
	}
	if x.Token == "" {
		return "", fmt.Errorf("registry token response empty")
	}
	return x.Token, nil
}
func (c *Client) do(ctx context.Context, ref ImageRef, path, accept, token string) (*http.Response, string, error) {
	u := "https://" + ref.Registry + path
	cred := c.credential(ref.Registry)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "vibewatch")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if strings.TrimSpace(cred.Secret) != "" {
		req.SetBasicAuth(cred.Username, cred.Secret)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, token, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, token, nil
	}
	resp.Body.Close()
	kind, ch := parseChallenge(resp.Header.Get("WWW-Authenticate"))
	if kind != "bearer" {
		return nil, token, fmt.Errorf("unsupported registry authentication")
	}
	if ch["scope"] == "" {
		ch["scope"] = "repository:" + ref.Repository + ":pull"
	}
	tok, err := c.token(ctx, ch, cred)
	if err != nil {
		return nil, "", err
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "vibewatch")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = c.HTTP.Do(req)
	return resp, tok, err
}
func (c *Client) getManifest(ctx context.Context, ref ImageRef, override, token string) (map[string]any, string, string, error) {
	target := ref.Tag
	if override != "" {
		target = override
	}
	accept := "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
	resp, tok, err := c.do(ctx, ref, "/v2/"+ref.Repository+"/manifests/"+url.PathEscape(target), accept, token)
	if err != nil {
		return nil, "", tok, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, "", tok, fmt.Errorf("registry manifest HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", tok, err
	}
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		sum := sha256.Sum256(b)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return m, digest, tok, nil
}
func (c *Client) getConfigLabels(ctx context.Context, ref ImageRef, digest, token string) (map[string]string, error) {
	resp, _, err := c.do(ctx, ref, "/v2/"+ref.Repository+"/blobs/"+url.PathEscape(digest), "application/octet-stream", token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("registry config HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var x struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(b, &x); err != nil {
		return nil, err
	}
	if x.Config.Labels == nil {
		x.Config.Labels = map[string]string{}
	}
	return x.Config.Labels, nil
}
