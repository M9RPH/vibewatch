package registry

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := map[string]ImageRef{
		"netbirdio/netbird:latest": {Registry: "registry-1.docker.io", Repository: "netbirdio/netbird", Tag: "latest"},
		"nginx":                    {Registry: "registry-1.docker.io", Repository: "library/nginx", Tag: "latest"},
		"ghcr.io/paperless-ngx/paperless-ngx:latest": {Registry: "ghcr.io", Repository: "paperless-ngx/paperless-ngx", Tag: "latest"},
		"ghcr.io/example/app:stable@sha256:abc123":   {Registry: "ghcr.io", Repository: "example/app", Tag: "sha256:abc123"},
	}
	for in, w := range cases {
		g, e := Parse(in)
		if e != nil || g != w {
			t.Fatalf("%s => %#v %v, want %#v", in, g, e, w)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRemoteDigestUsesManifestHeaderWithoutPullingConfig(t *testing.T) {
	calls := 0
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.Contains(r.URL.Path, "/v2/example/app/manifests/latest") {
			t.Fatalf("unexpected registry request: %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:remote"}},
			Body:       io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"}}`)),
			Request:    r,
		}, nil
	})}
	got, err := c.RemoteDigest(context.Background(), "fake.test/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:remote" {
		t.Fatalf("digest=%q", got)
	}
	if calls != 1 {
		t.Fatalf("RemoteDigest made %d requests; want one manifest request", calls)
	}
}

func TestRemoteDigestFallsBackToManifestHashWhenHeaderMissing(t *testing.T) {
	body := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	got, err := c.RemoteDigest(context.Background(), "fake.test/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
		t.Fatalf("unexpected fallback digest %q", got)
	}
}

func TestRemoteStateForPlatformSelectsTargetArchitecture(t *testing.T) {
	calls := []string{}
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "/manifests/latest"):
			body := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}}]}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Docker-Content-Digest": []string{"sha256:index"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		case strings.Contains(r.URL.Path, "/manifests/sha256:arm"):
			body := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:arm-config"}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Docker-Content-Digest": []string{"sha256:arm"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		case strings.Contains(r.URL.Path, "/manifests/sha256:amd"):
			t.Fatalf("amd64 manifest must not be selected for arm64 target")
		}
		t.Fatalf("unexpected registry request: %s", r.URL.String())
		return nil, nil
	})}

	got, err := c.RemoteStateForPlatform(context.Background(), "fake.test/example/app:latest", Platform{OS: "linux", Architecture: "aarch64"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigDigest != "sha256:arm-config" || got.ManifestDigest != "sha256:arm" || got.Platform.Architecture != "arm64" {
		t.Fatalf("unexpected remote state: %#v", got)
	}
	if len(calls) != 2 {
		t.Fatalf("requests=%v; want index + matching platform manifest only", calls)
	}
}

func TestChoosePlatformManifestVariantFallback(t *testing.T) {
	manifests := []map[string]any{
		{"digest": "sha256:arm", "platform": map[string]any{"os": "linux", "architecture": "arm64"}},
	}
	digest, platform, err := choosePlatformManifest(manifests, Platform{OS: "linux", Architecture: "arm64v8"})
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:arm" || platform.Architecture != "arm64" {
		t.Fatalf("digest=%q platform=%#v", digest, platform)
	}
}

func TestPrivateRegistryCredentialIsUsedForBearerTokenExchange(t *testing.T) {
	c := New()
	c.SetCredentials([]Credential{{Registry: "private.test", Username: "alice", Secret: "token-secret"}})
	calls := 0
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch {
		case r.URL.Host == "private.test":
			if calls == 1 {
				u, p, ok := r.BasicAuth()
				if !ok || u != "alice" || p != "token-secret" {
					t.Fatalf("manifest request missing configured basic auth")
				}
				return &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": []string{`Bearer realm="https://auth.private.test/token",service="private.test",scope="repository:example/app:pull"`}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			}
			if r.Header.Get("Authorization") != "Bearer issued-token" {
				t.Fatalf("manifest retry auth=%q", r.Header.Get("Authorization"))
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Docker-Content-Digest": []string{"sha256:private"}}, Body: io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)), Request: r}, nil
		case r.URL.Host == "auth.private.test":
			u, p, ok := r.BasicAuth()
			if !ok || u != "alice" || p != "token-secret" {
				t.Fatalf("token request missing configured basic auth")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"token":"issued-token"}`)), Request: r}, nil
		default:
			t.Fatalf("unexpected host %s", r.URL.Host)
			return nil, nil
		}
	})}
	got, err := c.RemoteDigest(context.Background(), "private.test/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:private" {
		t.Fatalf("digest=%q", got)
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
}

func TestRegistry429RetriesAndRecovers(t *testing.T) {
	var calls atomic.Int32
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n <= 2 {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader(`{"errors":[{"code":"TOOMANYREQUESTS"}]}`)),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:recovered"}},
			Body:       io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"}}`)),
			Request:    r,
		}, nil
	})}

	digest, err := c.RemoteDigest(context.Background(), "rate.test/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:recovered" {
		t.Fatalf("digest=%q", digest)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls=%d, want 3", got)
	}
}

func TestRegistry429FailsOnlyAfterAutomaticRetries(t *testing.T) {
	var calls atomic.Int32
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"0"}},
			Body:       io.NopCloser(strings.NewReader(`{"errors":[{"code":"TOOMANYREQUESTS"}]}`)),
			Request:    r,
		}, nil
	})}

	_, err := c.RemoteDigest(context.Background(), "rate.test/example/app:latest")
	if err == nil || !strings.Contains(err.Error(), "rate limited after automatic retries") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := calls.Load(); got != rateLimitMaxRetries+1 {
		t.Fatalf("calls=%d, want %d", got, rateLimitMaxRetries+1)
	}
}

func TestManifestCacheAvoidsRepeatedRegistryRequest(t *testing.T) {
	var calls atomic.Int32
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:cached"}},
			Body:       io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"}}`)),
			Request:    r,
		}, nil
	})}

	for i := 0; i < 2; i++ {
		if _, err := c.RemoteDigest(context.Background(), "cache.test/example/app:latest"); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("registry calls=%d, want 1 cached request", got)
	}
}

func TestManifestBurstIsCoalesced(t *testing.T) {
	var calls atomic.Int32
	requestStarted := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		startOnce.Do(func() { close(requestStarted) })
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:singleflight"}},
			Body:       io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"}}`)),
			Request:    r,
		}, nil
	})}

	const workers = 12
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := c.RemoteDigest(context.Background(), "burst.test/example/app:latest")
			errCh <- err
		}()
	}
	close(start)
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("registry request did not start")
	}
	// Give the remaining goroutines time to join the in-flight request before
	// the leader is allowed to complete.
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("registry calls=%d, want one coalesced request", got)
	}
}

type blockingTrackedBody struct {
	release <-chan struct{}
	reader  *strings.Reader
	active  *atomic.Int32
	once    sync.Once
	waited  bool
}

func (b *blockingTrackedBody) Read(p []byte) (int, error) {
	if !b.waited {
		b.waited = true
		<-b.release
	}
	return b.reader.Read(p)
}

func (b *blockingTrackedBody) Close() error {
	b.once.Do(func() { b.active.Add(-1) })
	return nil
}

func TestRegistryConcurrencyIsBoundedPerRegistry(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	c := New()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		body := &blockingTrackedBody{
			release: release,
			reader:  strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"}}`),
			active:  &active,
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:ok"}},
			Body:       body,
			Request:    r,
		}, nil
	})}

	const workers = 10
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := c.RemoteDigest(context.Background(), "limit.test/example/app"+strconv.Itoa(i)+":latest")
			errCh <- err
		}(i)
	}
	close(start)

	deadline := time.Now().Add(time.Second)
	for maximum.Load() < defaultRegistryConcurrency && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := maximum.Load(); got != defaultRegistryConcurrency {
		close(release)
		wg.Wait()
		t.Fatalf("maximum concurrent requests=%d, want %d", got, defaultRegistryConcurrency)
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := maximum.Load(); got > defaultRegistryConcurrency {
		t.Fatalf("maximum concurrent requests=%d, limit=%d", got, defaultRegistryConcurrency)
	}
}
