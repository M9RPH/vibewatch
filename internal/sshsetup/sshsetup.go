package sshsetup

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Client struct {
	SSHBinary     string
	SSHPassBinary string
	KnownHosts    string
}

type Result struct {
	IP       string `json:"ip"`
	Port     int    `json:"ssh_port"`
	Username string `json:"username"`
	Endpoint string `json:"endpoint"`
	Output   string `json:"output,omitempty"`
}

func New(dataDir string) *Client {
	return &Client{SSHBinary: "ssh", SSHPassBinary: "sshpass", KnownHosts: filepath.Join(dataDir, "ssh", "known_hosts")}
}

func Validate(ip, username string, port int) error {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("quick setup currently requires a valid IPv4 address")
	}
	if parsed.IsLoopback() || parsed.IsUnspecified() || parsed.IsMulticast() {
		return fmt.Errorf("the supplied IPv4 address is not a usable remote host address")
	}
	username = strings.TrimSpace(username)
	if username == "" || strings.ContainsAny(username, " \t\r\n@;|&$`<>(){}[]\\\"'") {
		return fmt.Errorf("invalid SSH username")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid SSH port")
	}
	return nil
}

func (c *Client) ConfigureDockerTCP(ctx context.Context, ip, username, password string, port int) (Result, error) {
	ip, username = strings.TrimSpace(ip), strings.TrimSpace(username)
	if err := Validate(ip, username, port); err != nil {
		return Result{}, err
	}
	if password == "" {
		return Result{}, fmt.Errorf("SSH password is required")
	}
	if strings.ContainsAny(password, "\r\n\x00") {
		return Result{}, fmt.Errorf("SSH passwords containing line breaks or NUL are not supported")
	}
	if err := os.MkdirAll(filepath.Dir(c.KnownHosts), 0o700); err != nil {
		return Result{}, err
	}

	uid, err := c.runSSH(ctx, ip, username, password, port, "id -u", "")
	if err != nil {
		return Result{}, fmt.Errorf("SSH login failed: %w", err)
	}
	asRoot := strings.TrimSpace(uid) == "0"
	if !asRoot {
		if _, err := c.runSSH(ctx, ip, username, password, port, "sudo -S -p '' true", password+"\n"); err != nil {
			return Result{}, fmt.Errorf("SSH login succeeded but sudo authentication failed: %w", err)
		}
	}

	script := `set -eu
BIND_IP="$1"
if ! command -v systemctl >/dev/null 2>&1; then
  echo "ERROR: systemd/systemctl is required"
  exit 41
fi
DOCKERD="$(command -v dockerd || true)"
if [ -z "$DOCKERD" ]; then
  echo "ERROR: dockerd was not found"
  exit 42
fi
if ! systemctl cat docker.service >/dev/null 2>&1; then
  echo "ERROR: docker.service was not found"
  exit 43
fi
BASE="$(systemctl cat docker.service | sed -n 's/^[[:space:]]*ExecStart=//p' | tail -n 1)"
if [ -z "$BASE" ]; then
  BASE="$DOCKERD"
fi
TARGET="tcp://${BIND_IP}:2375"
case " $BASE " in
  *" -H ${TARGET} "*|*" --host=${TARGET} "*|*" -H=${TARGET} "*)
    echo "Docker service already contains ${TARGET}"
    exit 0
    ;;
esac
mkdir -p /etc/systemd/system/docker.service.d
OVERRIDE=/etc/systemd/system/docker.service.d/vibewatch-tcp.conf
TMP="${OVERRIDE}.new"
cat > "$TMP" <<EOT
[Service]
ExecStart=
ExecStart=${BASE} -H ${TARGET}
EOT
mv "$TMP" "$OVERRIDE"
systemctl daemon-reload
if ! systemctl restart docker.service; then
  rm -f "$OVERRIDE"
  systemctl daemon-reload
  systemctl restart docker.service || true
  echo "ERROR: Docker failed to restart; Vibewatch override was rolled back"
  exit 44
fi
sleep 2
if ! docker info >/dev/null 2>&1; then
  rm -f "$OVERRIDE"
  systemctl daemon-reload
  systemctl restart docker.service || true
  echo "ERROR: Docker did not become healthy; Vibewatch override was rolled back"
  exit 45
fi
echo "Docker TCP endpoint enabled at ${TARGET}"
`
	payload := base64.StdEncoding.EncodeToString([]byte(script))
	remote := fmt.Sprintf("echo %s | base64 -d | sh -s -- %s", payload, ip)
	stdin := ""
	if !asRoot {
		remote = fmt.Sprintf("sudo -S -p '' sh -c %s", shellQuote(remote))
		stdin = password + "\n"
	}
	output, err := c.runSSH(ctx, ip, username, password, port, remote, stdin)
	if err != nil {
		return Result{}, fmt.Errorf("remote Docker setup failed: %w", err)
	}
	return Result{IP: ip, Port: port, Username: username, Endpoint: "tcp://" + ip + ":2375", Output: strings.TrimSpace(output)}, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func (c *Client) runSSH(ctx context.Context, ip, username, password string, port int, remoteCommand, stdin string) (string, error) {
	args := []string{
		"-e", c.SSHBinary,
		"-p", strconv.Itoa(port),
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + c.KnownHosts,
		username + "@" + ip,
		remoteCommand,
	}
	cmd := exec.CommandContext(ctx, c.SSHPassBinary, args...)
	cmd.Env = append(os.Environ(), "SSHPASS="+password)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
