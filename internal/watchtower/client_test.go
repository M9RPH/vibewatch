package watchtower

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
