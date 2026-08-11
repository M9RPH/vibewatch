package sshsetup

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	if err := Validate("192.168.1.20", "dennis", 22); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ip, user string
		port     int
	}{
		{"not-an-ip", "dennis", 22}, {"127.0.0.1", "dennis", 22}, {"192.168.1.2", "bad user", 22}, {"192.168.1.2", "root", 70000},
	} {
		if err := Validate(tc.ip, tc.user, tc.port); err == nil {
			t.Fatalf("expected error for %+v", tc)
		}
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("echo 'hello'")
	want := `'echo '\''hello'\'''`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConfigureDockerTCPDoesNotPutPasswordInArguments(t *testing.T) {
	dir := t.TempDir()
	logPath := dir + "/args.log"
	countPath := dir + "/count"
	script := dir + "/sshpass"
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
n=0
[ -f "` + countPath + `" ] && n=$(cat "` + countPath + `")
n=$((n+1)); echo "$n" > "` + countPath + `"
if [ "$n" -eq 1 ]; then echo 0; else echo 'Docker TCP endpoint enabled'; fi
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{SSHBinary: "ssh", SSHPassBinary: script, KnownHosts: dir + "/known_hosts"}
	res, err := c.ConfigureDockerTCP(context.Background(), "192.168.1.50", "dennis", "super-secret-password", 22)
	if err != nil {
		t.Fatal(err)
	}
	if res.Endpoint != "tcp://192.168.1.50:2375" {
		t.Fatalf("endpoint=%q", res.Endpoint)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "super-secret-password") {
		t.Fatalf("password leaked into process arguments: %s", b)
	}
}
