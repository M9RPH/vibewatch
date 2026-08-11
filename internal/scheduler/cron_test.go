package scheduler

import (
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	x := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	if !Match("0 4 * * *", x) {
		t.Fatal("expected match")
	}
	if Match("15 4 * * *", x) {
		t.Fatal("unexpected match")
	}
	if err := Validate("*/5 * * * *"); err != nil {
		t.Fatal(err)
	}
}
