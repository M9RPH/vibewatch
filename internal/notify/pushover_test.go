package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPushoverSendUsesPerUserAppTokenAndUserKey(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	defer ts.Close()
	p := NewPushover()
	p.Endpoint = ts.URL
	if err := p.Send(context.Background(), "user-app-token", "user-key", "Title", "Body"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"token=user-app-token", "user=user-key", "title=Title", "message=Body"} {
		if !strings.Contains(got, want) {
			t.Fatalf("request body %q missing %q", got, want)
		}
	}
}

func TestPushoverRequiresBothCredentials(t *testing.T) {
	p := NewPushover()
	if err := p.Send(context.Background(), "", "user-key", "Title", "Body"); err == nil {
		t.Fatal("expected missing app token error")
	}
	if err := p.Send(context.Background(), "app-token", "", "Title", "Body"); err == nil {
		t.Fatal("expected missing user key error")
	}
}
