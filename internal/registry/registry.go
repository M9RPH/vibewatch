package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Keep registry bursts deliberately small. A single Vibewatch controller can
	// fan out policy runs to many Docker hosts at once, but public registries see
	// those requests as one client/IP and can rate-limit a sudden manifest storm.
	defaultRegistryConcurrency = 3
	manifestCacheTTL           = 90 * time.Second
	rateLimitMaxRetries        = 3
)

var rateLimitBackoff = [...]time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

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
	Version string
	Source  string
}

type cached struct {
	V  Version
	At time.Time
}

type cachedManifest struct {
	Manifest map[string]any
	Digest   string
	Token    string
	At       time.Time
}

type manifestFlight struct {
	done     chan struct{}
	manifest map[string]any
	digest   string
	token    string
	err      error
}

type registryLimiter struct {
	sem chan struct{}
	mu  sync.Mutex

	blockedUntil time.Time
}

type Credential struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"-"`
}

type Client struct {
	HTTP            *http.Client
	mu              sync.Mutex
	cache           map[string]cached
	manifestCache   map[string]cachedManifest
	manifestFlights map[string]*manifestFlight
	creds           map[string]Credential
	authGeneration  uint64
	limiters        map[string]*registryLimiter
}

func New() *Client {
	return &Client{
		HTTP:            &http.Client{Timeout: 15 * time.Second},
		cache:           map[string]cached{},
		manifestCache:   map[string]cachedManifest{},
		manifestFlights: map[string]*manifestFlight{},
		creds:           map[string]Credential{},
		limiters:        map[string]*registryLimiter{},
	}
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
	c.manifestCache = map[string]cachedManifest{}
	c.authGeneration++
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
	manifest, _, auth, _, err := c.getPlatformManifest(ctx, ref, p)
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
	out := Version{Version: version, Source: source}
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

type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *releaseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func (c *Client) limiter(registry string) *registryLimiter {
	registry = normalizeRegistryHost(registry)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limiters == nil {
		c.limiters = map[string]*registryLimiter{}
	}
	if lim := c.limiters[registry]; lim != nil {
		return lim
	}
	lim := &registryLimiter{sem: make(chan struct{}, defaultRegistryConcurrency)}
	c.limiters[registry] = lim
	return lim
}

func (l *registryLimiter) wait(ctx context.Context) error {
	l.mu.Lock()
	wait := time.Until(l.blockedUntil)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	return sleepContext(ctx, wait)
}

func (l *registryLimiter) coolDown(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d)
	l.mu.Lock()
	if until.After(l.blockedUntil) {
		l.blockedUntil = until
	}
	l.mu.Unlock()
}

func (c *Client) httpDo(ctx context.Context, registry string, req *http.Request) (*http.Response, error) {
	lim := c.limiter(registry)
	select {
	case lim.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-lim.sem }
	if err := lim.wait(ctx); err != nil {
		release()
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		release()
		return nil, err
	}
	if resp.Body == nil {
		release()
		return resp, nil
	}
	resp.Body = &releaseBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

func retryAfterDelay(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(v); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func rateLimitDelay(resp *http.Response, retry int) time.Duration {
	if resp != nil {
		if d, ok := retryAfterDelay(resp.Header.Get("Retry-After"), time.Now()); ok {
			return d
		}
	}
	if retry < 0 {
		retry = 0
	}
	if retry >= len(rateLimitBackoff) {
		retry = len(rateLimitBackoff) - 1
	}
	// Jitter prevents simultaneous jobs that were released from the same
	// registry bucket from retrying on exactly the same millisecond.
	return rateLimitBackoff[retry] + time.Duration(rand.Intn(501))*time.Millisecond
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) httpDoRateLimited(ctx context.Context, registry string, makeRequest func() (*http.Request, error)) (*http.Response, error) {
	lim := c.limiter(registry)
	for attempt := 0; ; attempt++ {
		req, err := makeRequest()
		if err != nil {
			return nil, err
		}
		resp, err := c.httpDo(ctx, registry, req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= rateLimitMaxRetries {
			return resp, nil
		}
		delay := rateLimitDelay(resp, attempt)
		// A 429 applies to the registry/client as a whole, not just one container.
		// Publish the cooldown to every concurrent policy run before this request
		// releases its slot, so new work waits instead of immediately hammering the
		// provider with the next manifest request.
		lim.coolDown(delay)
		// Drain a bounded response so keep-alive connections remain reusable, then
		// release the per-registry slot before waiting for the next attempt.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
		_ = resp.Body.Close()
	}
}

func (c *Client) token(ctx context.Context, registry string, ch map[string]string, cred Credential) (string, error) {
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
	resp, err := c.httpDoRateLimited(ctx, registry, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "vibewatch")
		if strings.TrimSpace(cred.Secret) != "" {
			req.SetBasicAuth(cred.Username, cred.Secret)
		}
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", fmt.Errorf("registry token HTTP 429 (rate limited after automatic retries): %s", strings.TrimSpace(string(b)))
		}
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
	makeRequest := func(authToken string) func() (*http.Request, error) {
		return func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "vibewatch")
			if accept != "" {
				req.Header.Set("Accept", accept)
			}
			if authToken != "" {
				req.Header.Set("Authorization", "Bearer "+authToken)
			} else if strings.TrimSpace(cred.Secret) != "" {
				req.SetBasicAuth(cred.Username, cred.Secret)
			}
			return req, nil
		}
	}
	resp, err := c.httpDoRateLimited(ctx, ref.Registry, makeRequest(token))
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
	tok, err := c.token(ctx, ref.Registry, ch, cred)
	if err != nil {
		return nil, "", err
	}
	resp, err = c.httpDoRateLimited(ctx, ref.Registry, makeRequest(tok))
	return resp, tok, err
}

func (c *Client) manifestKey(ref ImageRef, target string) string {
	c.mu.Lock()
	generation := c.authGeneration
	c.mu.Unlock()
	return fmt.Sprintf("%d|%s/%s@%s", generation, normalizeRegistryHost(ref.Registry), ref.Repository, target)
}

func (c *Client) cachedManifest(key string) (cachedManifest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manifestCache == nil {
		c.manifestCache = map[string]cachedManifest{}
	}
	x, ok := c.manifestCache[key]
	if !ok {
		return cachedManifest{}, false
	}
	if time.Since(x.At) >= manifestCacheTTL {
		delete(c.manifestCache, key)
		return cachedManifest{}, false
	}
	return x, true
}

func (c *Client) beginManifestFlight(key string) (*manifestFlight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manifestFlights == nil {
		c.manifestFlights = map[string]*manifestFlight{}
	}
	if flight := c.manifestFlights[key]; flight != nil {
		return flight, false
	}
	flight := &manifestFlight{done: make(chan struct{})}
	c.manifestFlights[key] = flight
	return flight, true
}

func (c *Client) finishManifestFlight(key string, flight *manifestFlight, manifest map[string]any, digest, token string, err error) {
	c.mu.Lock()
	flight.manifest, flight.digest, flight.token, flight.err = manifest, digest, token, err
	if err == nil {
		if c.manifestCache == nil {
			c.manifestCache = map[string]cachedManifest{}
		}
		c.manifestCache[key] = cachedManifest{Manifest: manifest, Digest: digest, Token: token, At: time.Now()}
	}
	delete(c.manifestFlights, key)
	close(flight.done)
	c.mu.Unlock()
}

func (c *Client) getManifest(ctx context.Context, ref ImageRef, override, token string) (map[string]any, string, string, error) {
	target := ref.Tag
	if override != "" {
		target = override
	}
	key := c.manifestKey(ref, target)
	if x, ok := c.cachedManifest(key); ok {
		return x.Manifest, x.Digest, x.Token, nil
	}
	flight, leader := c.beginManifestFlight(key)
	if !leader {
		select {
		case <-flight.done:
			return flight.manifest, flight.digest, flight.token, flight.err
		case <-ctx.Done():
			return nil, "", token, ctx.Err()
		}
	}

	accept := "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
	resp, tok, err := c.do(ctx, ref, "/v2/"+ref.Repository+"/manifests/"+url.PathEscape(target), accept, token)
	if err != nil {
		c.finishManifestFlight(key, flight, nil, "", tok, err)
		return nil, "", tok, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var responseErr error
		if resp.StatusCode == http.StatusTooManyRequests {
			responseErr = fmt.Errorf("registry manifest HTTP 429 (rate limited after automatic retries): %s", strings.TrimSpace(string(b)))
		} else {
			responseErr = fmt.Errorf("registry manifest HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		c.finishManifestFlight(key, flight, nil, "", tok, responseErr)
		return nil, "", tok, responseErr
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		c.finishManifestFlight(key, flight, nil, "", tok, err)
		return nil, "", tok, err
	}
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		sum := sha256.Sum256(b)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	c.finishManifestFlight(key, flight, m, digest, tok, nil)
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
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("registry config HTTP 429 (rate limited after automatic retries): %s", strings.TrimSpace(string(b)))
		}
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
