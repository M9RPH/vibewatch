package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("wrong password verified")
	}
}
