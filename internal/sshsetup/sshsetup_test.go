package sshsetup

import (
	"context"
	"os"
	"os/exec"
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
	if !transactionTokenRE.MatchString(res.TransactionToken) {
		t.Fatalf("invalid transaction token %q", res.TransactionToken)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "super-secret-password") {
		t.Fatalf("password leaked into process arguments: %s", b)
	}
}

func TestConfigureDockerMTLSKeepsSecretsOutOfArguments(t *testing.T) {
	dir := t.TempDir()
	logPath := dir + "/args.log"
	countPath := dir + "/count"
	script := dir + "/sshpass"
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
n=0
[ -f "` + countPath + `" ] && n=$(cat "` + countPath + `")
n=$((n+1)); echo "$n" > "` + countPath + `"
if [ "$n" -eq 1 ]; then echo 0; fi
if [ "$n" -eq 5 ]; then echo 'Secure Docker endpoint enabled'; fi
if [ "$n" -eq 6 ]; then printf '%s\n' '-----BEGIN CERTIFICATE-----' 'TEST-CA-SECRET' '-----END CERTIFICATE-----'; fi
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{SSHBinary: "ssh", SSHPassBinary: script, KnownHosts: dir + "/known_hosts"}
	ca := "-----BEGIN CERTIFICATE-----\nTEST-CA-SECRET\n-----END CERTIFICATE-----\n"
	cert := "-----BEGIN CERTIFICATE-----\nTEST-SERVER-CERT\n-----END CERTIFICATE-----\n"
	key := "-----BEGIN PRIVATE KEY-----\nTEST-SERVER-PRIVATE-KEY\n-----END PRIVATE KEY-----\n"
	res, err := c.ConfigureDockerMTLS(context.Background(), "192.168.1.60", "dennis", "super-secret-password", 22, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.Endpoint != "tls://192.168.1.60:2376" {
		t.Fatalf("endpoint=%q", res.Endpoint)
	}
	if !transactionTokenRE.MatchString(res.TransactionToken) {
		t.Fatalf("invalid transaction token %q", res.TransactionToken)
	}
	if !strings.Contains(res.CAPEM, "TEST-CA-SECRET") {
		t.Fatalf("effective CA was not read back: %q", res.CAPEM)
	}
	b, _ := os.ReadFile(logPath)
	args := string(b)
	for _, secret := range []string{"super-secret-password", "TEST-CA-SECRET", "TEST-SERVER-CERT", "TEST-SERVER-PRIVATE-KEY"} {
		if strings.Contains(args, secret) {
			t.Fatalf("secret leaked into process arguments: %q", secret)
		}
	}
}

func TestRunSSHPreservesStdoutDiagnosticsWhenSSHAlsoWritesStderr(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/sshpass"
	body := `#!/bin/sh
printf '%s\n' 'Job for docker.service failed' >&2
printf '%s\n' 'Docker rejected the staged mTLS configuration.'
printf '%s\n' 'dockerd: the following directives are specified both as a flag and in the configuration file: hosts'
printf '%s\n' 'Rollback successful: previous Docker configuration restored.'
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{SSHBinary: "ssh", SSHPassBinary: script, KnownHosts: dir + "/known_hosts"}
	_, err := c.runSSH(context.Background(), "192.168.1.60", "root", "secret", 22, "true", "")
	if err == nil {
		t.Fatal("expected SSH error")
	}
	text := err.Error()
	for _, want := range []string{
		"Job for docker.service failed",
		"Docker rejected the staged mTLS configuration",
		"specified both as a flag",
		"Rollback successful",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("combined SSH error missing %q:\n%s", want, text)
		}
	}
}

func TestQuickSetupScriptsGuardCommonDockerConfigurationConflicts(t *testing.T) {
	for name, script := range map[string]string{"tcp": configureDockerTCPScript, "secure": configureDockerMTLSScript} {
		for _, want := range []string{
			`"hosts"[[:space:]]*:`,
			"-H unix:///var/run/docker.sock",
			"systemctl status docker.service",
			"journalctl -u docker.service",
			"Quick-setup transaction",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s quick-setup script missing safety behavior %q", name, want)
			}
		}
	}
	for _, want := range []string{"--tlsverify", "port_listening 2376", "MANAGED_EXISTING"} {
		if !strings.Contains(configureDockerMTLSScript, want) {
			t.Fatalf("secure quick-setup script missing %q", want)
		}
	}
	for _, want := range []string{"port_listening 2375", "Legacy TCP Quick Setup will not add an unencrypted endpoint"} {
		if !strings.Contains(configureDockerTCPScript, want) {
			t.Fatalf("tcp quick-setup script missing %q", want)
		}
	}
}

func TestQuickSetupTransactionCommitAndRollbackKeepPasswordOutOfArguments(t *testing.T) {
	dir := t.TempDir()
	logPath := dir + "/args.log"
	script := dir + "/sshpass"
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  *" id -u") echo 0 ;;
  *) echo ok ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{SSHBinary: "ssh", SSHPassBinary: script, KnownHosts: dir + "/known_hosts"}
	token := "0123456789abcdef01234567"
	if _, err := c.CommitDockerSetup(context.Background(), "192.168.1.60", "root", "super-secret-password", 22, token); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RollbackDockerSetup(context.Background(), "192.168.1.60", "root", "super-secret-password", 22, token, "secure"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "super-secret-password") {
		t.Fatalf("password leaked into transaction process arguments: %s", b)
	}
}

func TestQuickSetupShellScriptsParse(t *testing.T) {
	for name, script := range map[string]string{
		"tcp":         configureDockerTCPScript,
		"secure":      configureDockerMTLSScript,
		"rollback":    rollbackDockerSetupScript,
		"diagnostics": dockerDiagnosticScript,
	} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s shell script failed sh -n: %v\n%s", name, err, out)
		}
	}
}

func TestSecureEndpointFromOutputSupportsDaemonJSONListenerPort(t *testing.T) {
	got := secureEndpointFromOutput("staged\nVIBEWATCH_SECURE_ENDPOINT=tls://192.168.1.206:2375\nready", "192.168.1.206")
	if got != "tls://192.168.1.206:2375" {
		t.Fatalf("endpoint=%q", got)
	}
	if got := secureEndpointFromOutput("VIBEWATCH_SECURE_ENDPOINT=tls://192.168.1.99:2375", "192.168.1.206"); got != "tls://192.168.1.206:2376" {
		t.Fatalf("foreign endpoint marker should be ignored, got %q", got)
	}
}

func TestEffectiveExecStartParserCollapsesSystemdLineContinuations(t *testing.T) {
	unit := `# /lib/systemd/system/docker.service
[Service]
ExecStart=/usr/bin/dockerd -H fd:// --containerd=/run/containerd/containerd.sock
# /etc/systemd/system/docker.service.d/override.conf
[Service]
ExecStart=
ExecStart=/usr/bin/dockerd \
  -H unix:///var/run/docker.sock \
  -H tcp://192.168.1.206:2375 \
  --containerd=/run/containerd/containerd.sock
`
	cmd := exec.Command("sh", "-c", effectiveExecStartShellFunction+"\neffective_execstart")
	cmd.Stdin = strings.NewReader(unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("parser failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	want := "/usr/bin/dockerd -H unix:///var/run/docker.sock -H tcp://192.168.1.206:2375 --containerd=/run/containerd/containerd.sock"
	if got != want {
		t.Fatalf("effective ExecStart=%q want %q", got, want)
	}
	if strings.Contains(got, `\\`) {
		t.Fatalf("effective ExecStart still contains a continuation backslash: %q", got)
	}
}
