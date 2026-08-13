package watchtower

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckAllOmitsContainerFilter(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"containers":[],"count":0}`))
	}))
	defer srv.Close()

	c := New()
	if _, _, err := c.Check(context.Background(), srv.URL, "token", ""); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/check" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotQuery != "" {
		t.Fatalf("bulk check must not add a container filter, got query %q", gotQuery)
	}
}

func TestCheckSingleAddsContainerFilter(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("container")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"containers":[],"count":0}`))
	}))
	defer srv.Close()

	c := New()
	if _, _, err := c.Check(context.Background(), srv.URL, "token", "paperless web"); err != nil {
		t.Fatal(err)
	}
	if got != "paperless web" {
		t.Fatalf("unexpected container query: %q", got)
	}
}

func TestWaitReadyForToleratesSlowWorkerStartup(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitReadyFor(ctx, srv.URL, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 3 {
		t.Fatalf("expected readiness retries, got %d", calls.Load())
	}
}
