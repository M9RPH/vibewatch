package sshsetup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	SSHBinary     string
	SSHPassBinary string
	KnownHosts    string
}

type Result struct {
	IP               string `json:"ip"`
	Port             int    `json:"ssh_port"`
	Username         string `json:"username"`
	Endpoint         string `json:"endpoint"`
	Output           string `json:"output,omitempty"`
	CAPEM            string `json:"-"`
	TransactionToken string `json:"-"`
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

var transactionTokenRE = regexp.MustCompile(`^[a-f0-9]{24}$`)

func newTransactionToken() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validateTransactionToken(token string) error {
	if !transactionTokenRE.MatchString(strings.TrimSpace(token)) {
		return fmt.Errorf("invalid quick-setup transaction token")
	}
	return nil
}

func (c *Client) ensureSSHDir() error {
	return os.MkdirAll(filepath.Dir(c.KnownHosts), 0o700)
}

func (c *Client) rootMode(ctx context.Context, ip, username, password string, port int) (bool, error) {
	uid, err := c.runSSH(ctx, ip, username, password, port, "id -u", "")
	if err != nil {
		return false, fmt.Errorf("SSH login failed: %w", err)
	}
	asRoot := strings.TrimSpace(uid) == "0"
	if !asRoot {
		if _, err := c.runSSH(ctx, ip, username, password, port, "sudo -S -p '' true", password+"\n"); err != nil {
			return false, fmt.Errorf("SSH login succeeded but sudo authentication failed: %w", err)
		}
	}
	return asRoot, nil
}

func privilegedCommand(command, password string, asRoot bool) (remote, stdin string) {
	if asRoot {
		return command, ""
	}
	return fmt.Sprintf("sudo -S -p '' sh -c %s", shellQuote(command)), password + "\n"
}

const effectiveExecStartShellFunction = `effective_execstart() {
  awk '
    /^[[:space:]]*ExecStart=/ {
      line=$0
      sub(/^[[:space:]]*ExecStart=/, "", line)
      if (line ~ /^[[:space:]]*$/) { last=""; next }
      cmd=line
      while (cmd ~ /\\[[:space:]]*$/) {
        sub(/[[:space:]]*\\[[:space:]]*$/, "", cmd)
        if ((getline more) <= 0) break
        sub(/^[[:space:]]+/, "", more)
        cmd=cmd " " more
      }
      last=cmd
    }
    END { print last }
  '
}`

const dockerDiagnosticScript = `set +e
echo "--- Vibewatch Docker diagnostics ---"
echo "service state: $(systemctl is-active docker.service 2>/dev/null || echo unknown)"
echo "service enabled: $(systemctl is-enabled docker.service 2>/dev/null || echo unknown)"
echo "effective Docker unit / ExecStart:"
systemctl cat docker.service 2>&1 | tail -n 90 || true
if command -v effective_execstart >/dev/null 2>&1; then
  echo "logical effective ExecStart:"
  systemctl cat docker.service 2>/dev/null | effective_execstart || true
fi
if [ -f /etc/docker/daemon.json ]; then
  echo "daemon.json relevant keys:"
  grep -E '"(hosts|tls|tlsverify|tlscacert|tlscert|tlskey)"[[:space:]]*:' /etc/docker/daemon.json 2>/dev/null | head -n 30 || true
fi
echo "listeners on Docker remote ports:"
if command -v ss >/dev/null 2>&1; then
  ss -ltnp 2>/dev/null | grep -E ':(2375|2376)([[:space:]]|$)' || true
elif command -v netstat >/dev/null 2>&1; then
  netstat -ltnp 2>/dev/null | grep -E ':(2375|2376)([[:space:]]|$)' || true
fi
echo "docker.service status:"
systemctl status docker.service --no-pager -l 2>&1 | tail -n 55 || true
echo "recent docker.service journal:"
journalctl -u docker.service --no-pager -n 70 2>&1 | tail -n 70 || true
echo "--- End Vibewatch Docker diagnostics ---"`

const configureDockerTCPScript = `set -eu
BIND_IP="$1"
TXN="$2"
TARGET="tcp://${BIND_IP}:2375"
DROPIN_DIR=/etc/systemd/system/docker.service.d
OVERRIDE=${DROPIN_DIR}/vibewatch-tcp.conf
MTLS=${DROPIN_DIR}/vibewatch-mtls.conf
BACKUP="/tmp/vibewatch-quicksetup-${TXN}"

fail() { echo "ERROR: $1"; exit "${2:-40}"; }
port_listening() {
  P="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltnH 2>/dev/null | awk '{print $4}' | grep -Eq "(:|\\])${P}$"
    return $?
  fi
  if command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk 'NR>2 {print $4}' | grep -Eq "(:|\\])${P}$"
    return $?
  fi
  return 1
}
` + effectiveExecStartShellFunction + `
local_rollback() {
  if [ ! -d "$BACKUP" ]; then return 0; fi
  if [ -f "$BACKUP/had-tcp" ]; then
    cp -a "$BACKUP/tcp.conf" "$OVERRIDE"
  else
    rm -f "$OVERRIDE"
  fi
  systemctl daemon-reload
  systemctl restart docker.service
}
collect_diagnostics() {
` + dockerDiagnosticScript + `
}

case "$TXN" in *[!a-f0-9]*|'') fail "invalid quick-setup transaction token" 40 ;; esac
if ! command -v systemctl >/dev/null 2>&1; then fail "systemd/systemctl is required" 41; fi
DOCKERD="$(command -v dockerd || true)"
DOCKER="$(command -v docker || true)"
if [ -z "$DOCKERD" ]; then fail "dockerd was not found" 42; fi
if [ -z "$DOCKER" ]; then fail "Docker CLI was not found; it is required for safe post-change validation" 42; fi
if ! systemctl cat docker.service >/dev/null 2>&1; then fail "docker.service was not found" 43; fi
if ! systemctl is-active --quiet docker.service; then fail "docker.service is not running; start Docker before using Quick Setup" 47; fi
if command -v ip >/dev/null 2>&1 && ! ip -4 addr show 2>/dev/null | grep -Fq " ${BIND_IP}/"; then
  fail "${BIND_IP} is not assigned to this Docker host; use an address present on a local interface" 48
fi
FULL_UNIT="$(systemctl cat docker.service)"
DAEMON_HOSTS=0
DAEMON_TLS=0
if [ -f /etc/docker/daemon.json ]; then
  grep -Eq '"hosts"[[:space:]]*:' /etc/docker/daemon.json && DAEMON_HOSTS=1 || true
  grep -Eq '"tls"[[:space:]]*:[[:space:]]*true|"tlsverify"[[:space:]]*:[[:space:]]*true|"tlscacert"|"tlscert"|"tlskey"' /etc/docker/daemon.json && DAEMON_TLS=1 || true
fi
UNIT_TLS=0
printf '%s\n' "$FULL_UNIT" | grep -Eq -- '--tlsverify|--tlscacert|--tlscert|--tlskey' && UNIT_TLS=1 || true
if [ "$DAEMON_TLS" -eq 1 ] || [ "$UNIT_TLS" -eq 1 ] || [ -f "$MTLS" ]; then
  fail "Docker already has TLS/mTLS configuration. Legacy TCP Quick Setup will not add an unencrypted endpoint beside an existing secure listener." 46
fi
# If Docker already serves this exact endpoint, do not rewrite its configuration.
if "$DOCKER" --host "$TARGET" version --format '{{.Server.Version}}' >/dev/null 2>&1; then
  echo "Docker TCP endpoint already available at ${TARGET}; no daemon changes were required"
  exit 0
fi
if [ "$DAEMON_HOSTS" -eq 1 ]; then
  fail "Docker daemon.json already defines 'hosts'. Vibewatch will not duplicate the hosts option through systemd because dockerd rejects mixed flag/config definitions. Remove or migrate that hosts entry before Quick Setup." 49
fi
if port_listening 2375; then
  fail "TCP port 2375 is already in use, but it is not responding as the Docker endpoint ${TARGET}. No changes were made." 50
fi
BASE="$(printf '%s\n' "$FULL_UNIT" | effective_execstart)"
if [ -z "$BASE" ]; then BASE="$DOCKERD"; fi
# Explicit -H flags disable dockerd's implicit Unix socket. Preserve local Docker
# administration on units that previously relied on the implicit default.
if ! printf '%s\n' "$BASE" | grep -Eq '(^|[[:space:]])(-H([=[:space:]])|--host([=[:space:]]))'; then
  BASE="${BASE} -H unix:///var/run/docker.sock"
fi
rm -rf "$BACKUP"
install -d -m 0700 "$BACKUP"
if [ -f "$OVERRIDE" ]; then touch "$BACKUP/had-tcp"; cp -a "$OVERRIDE" "$BACKUP/tcp.conf"; fi
mkdir -p "$DROPIN_DIR"
TMP="${OVERRIDE}.new"
cat > "$TMP" <<EOT
[Service]
ExecStart=
ExecStart=${BASE} -H ${TARGET}
EOT
mv "$TMP" "$OVERRIDE"
systemctl daemon-reload
EFFECTIVE="$(systemctl cat docker.service | effective_execstart)"
if ! printf '%s\n' "$EFFECTIVE" | grep -Fq -- "-H ${TARGET}" && ! printf '%s\n' "$EFFECTIVE" | grep -Fq -- "-H=${TARGET}" && ! printf '%s\n' "$EFFECTIVE" | grep -Fq -- "--host=${TARGET}"; then
  if [ -f "$BACKUP/had-tcp" ]; then cp -a "$BACKUP/tcp.conf" "$OVERRIDE"; else rm -f "$OVERRIDE"; fi
  systemctl daemon-reload
  rm -rf "$BACKUP"
  fail "another docker.service drop-in overrides Vibewatch's staged TCP ExecStart; no service restart was attempted" 51
fi
if ! systemctl restart docker.service; then
  echo "Docker rejected the staged TCP configuration. Capturing diagnostics before rollback..."
  collect_diagnostics
  if local_rollback; then
    echo "Rollback successful: the previous Docker configuration was restored and Docker restarted."
  else
    echo "CRITICAL: rollback could not restart Docker. Manual recovery is required."
    collect_diagnostics
  fi
  rm -rf "$BACKUP"
  fail "Docker failed to restart with the staged TCP configuration; see diagnostics above" 44
fi
sleep 2
if ! "$DOCKER" info >/dev/null 2>&1 || ! "$DOCKER" --host "$TARGET" version --format '{{.Server.Version}}' >/dev/null 2>&1; then
  echo "Docker restarted but local or remote API validation failed. Capturing diagnostics before rollback..."
  collect_diagnostics
  if local_rollback; then
    echo "Rollback successful: the previous Docker configuration was restored and Docker restarted."
  else
    echo "CRITICAL: rollback could not restart Docker. Manual recovery is required."
    collect_diagnostics
  fi
  rm -rf "$BACKUP"
  fail "Docker did not pass local + TCP endpoint validation; the staged change was rolled back" 45
fi
echo "Docker TCP endpoint staged at ${TARGET}"
echo "Quick-setup transaction ${TXN} is ready for controller reachability validation"
`

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
	if err := c.ensureSSHDir(); err != nil {
		return Result{}, err
	}
	asRoot, err := c.rootMode(ctx, ip, username, password, port)
	if err != nil {
		return Result{}, err
	}
	token, err := newTransactionToken()
	if err != nil {
		return Result{}, fmt.Errorf("generate quick-setup transaction: %w", err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte(configureDockerTCPScript))
	remote := fmt.Sprintf("echo %s | base64 -d | sh -s -- %s %s", payload, shellQuote(ip), shellQuote(token))
	remote, stdin := privilegedCommand(remote, password, asRoot)
	output, err := c.runSSH(ctx, ip, username, password, port, remote, stdin)
	if err != nil {
		return Result{}, fmt.Errorf("remote Docker setup failed: %w", err)
	}
	return Result{IP: ip, Port: port, Username: username, Endpoint: "tcp://" + ip + ":2375", Output: strings.TrimSpace(output), TransactionToken: token}, nil
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
	// Keep stdout and stderr in one ordered stream. systemctl commonly writes the
	// one-line failure to stderr while Vibewatch's diagnostic/rollback detail is
	// on stdout; choosing only one stream previously hid the actual dockerd cause.
	var combined bytes.Buffer
	cmd.Stdout, cmd.Stderr = &combined, &combined
	err := cmd.Run()
	msg := strings.TrimSpace(combined.String())
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return msg, nil
}

const configureDockerMTLSScript = `set -eu
BIND_IP="$1"
SRC_CA="$2"
SRC_CERT="$3"
SRC_KEY="$4"
TXN="$5"
TLS_DIR=/etc/docker/vibewatch-tls
DROPIN_DIR=/etc/systemd/system/docker.service.d
OVERRIDE=${DROPIN_DIR}/vibewatch-mtls.conf
LEGACY=${DROPIN_DIR}/vibewatch-tcp.conf
BACKUP="/tmp/vibewatch-quicksetup-${TXN}"
cleanup_uploads() { rm -f "$SRC_CA" "$SRC_CERT" "$SRC_KEY"; }
trap cleanup_uploads EXIT
fail() { echo "ERROR: $1"; exit "${2:-40}"; }
port_listening() {
  P="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltnH 2>/dev/null | awk '{print $4}' | grep -Eq "(:|\\])${P}$"
    return $?
  fi
  if command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk 'NR>2 {print $4}' | grep -Eq "(:|\\])${P}$"
    return $?
  fi
  return 1
}
` + effectiveExecStartShellFunction + `
collect_diagnostics() {
` + dockerDiagnosticScript + `
}
local_rollback() {
  if [ ! -d "$BACKUP" ]; then return 0; fi
  rm -f "$OVERRIDE" "$LEGACY"
  rm -rf "$TLS_DIR"
  [ -f "$BACKUP/had-mtls" ] && cp -a "$BACKUP/mtls.conf" "$OVERRIDE" || true
  [ -f "$BACKUP/had-tcp" ] && cp -a "$BACKUP/tcp.conf" "$LEGACY" || true
  [ -f "$BACKUP/had-tlsdir" ] && cp -a "$BACKUP/tls" "$TLS_DIR" || true
  systemctl daemon-reload
  systemctl restart docker.service
}

case "$TXN" in *[!a-f0-9]*|'') fail "invalid quick-setup transaction token" 40 ;; esac
if ! command -v systemctl >/dev/null 2>&1; then fail "systemd/systemctl is required" 41; fi
DOCKERD="$(command -v dockerd || true)"
DOCKER="$(command -v docker || true)"
if [ -z "$DOCKERD" ]; then fail "dockerd was not found" 42; fi
if [ -z "$DOCKER" ]; then fail "Docker CLI was not found; it is required for safe post-change validation" 42; fi
if ! systemctl cat docker.service >/dev/null 2>&1; then fail "docker.service was not found" 43; fi
if ! systemctl is-active --quiet docker.service; then fail "docker.service is not running; start Docker before using Secure Quick Setup" 47; fi
if command -v ip >/dev/null 2>&1 && ! ip -4 addr show 2>/dev/null | grep -Fq " ${BIND_IP}/"; then
  fail "${BIND_IP} is not assigned to this Docker host; use an address present on a local interface" 48
fi
FULL_UNIT="$(systemctl cat docker.service)"
MANAGED_EXISTING=0
if [ -f "$OVERRIDE" ] && [ -s "$TLS_DIR/ca.pem" ] && [ -s "$TLS_DIR/server-cert.pem" ] && [ -s "$TLS_DIR/server-key.pem" ]; then
  MANAGED_EXISTING=1
fi
DAEMON_HOSTS=0
DAEMON_TLS=0
if [ -f /etc/docker/daemon.json ]; then
  grep -Eq '"hosts"[[:space:]]*:' /etc/docker/daemon.json && DAEMON_HOSTS=1 || true
  grep -Eq '"tls"[[:space:]]*:[[:space:]]*true|"tlsverify"[[:space:]]*:[[:space:]]*true|"tlscacert"|"tlscert"|"tlskey"' /etc/docker/daemon.json && DAEMON_TLS=1 || true
fi
UNIT_TLS=0
printf '%s\n' "$FULL_UNIT" | grep -Eq -- '--tlsverify|--tlscacert|--tlscert|--tlskey' && UNIT_TLS=1 || true
if [ "$DAEMON_TLS" -eq 1 ]; then
  fail "Docker daemon.json already owns TLS settings. Vibewatch will not mix its systemd-managed mTLS listener with daemon.json TLS configuration; use Advanced connection setup for this host." 46
fi
if [ "$UNIT_TLS" -eq 1 ] && [ "$MANAGED_EXISTING" -ne 1 ]; then
  fail "An existing Docker TLS/mTLS systemd configuration is owned outside Vibewatch. It was left untouched; use Advanced connection setup with that CA/client identity." 46
fi
SECURE_PORT=2376
USE_DAEMON_HOSTS=0
if [ "$DAEMON_HOSTS" -eq 1 ]; then
  # Do not duplicate the hosts directive through command-line flags. If the
  # user-owned daemon.json already exposes a TCP Docker listener, secure that
  # existing listener in place by adding only the TLS flags. This supports the
  # common daemon.json-based 2375 setup without rewriting the JSON document.
  COMPACT_DAEMON="$(tr -d '[:space:]' < /etc/docker/daemon.json 2>/dev/null || true)"
  case "$COMPACT_DAEMON" in
    *"tcp://${BIND_IP}:2376"*|*"tcp://0.0.0.0:2376"*|*"tcp://[::]:2376"*|*"tcp://:::2376"*|*"tcp://:2376"*) SECURE_PORT=2376; USE_DAEMON_HOSTS=1 ;;
    *"tcp://${BIND_IP}:2375"*|*"tcp://0.0.0.0:2375"*|*"tcp://[::]:2375"*|*"tcp://:::2375"*|*"tcp://:2375"*) SECURE_PORT=2375; USE_DAEMON_HOSTS=1 ;;
    *) fail "Docker daemon.json defines 'hosts', but no TCP listener on 2375/2376 bound to this host can be safely reused. Vibewatch will not rewrite user-owned daemon.json automatically." 49 ;;
  esac
fi
if [ "$USE_DAEMON_HOSTS" -eq 0 ] && [ "$MANAGED_EXISTING" -ne 1 ] && port_listening 2376; then
  fail "TCP port 2376 is already in use by an existing listener not managed by Vibewatch. No changes were made." 50
fi
BASE="$(printf '%s\n' "$FULL_UNIT" | effective_execstart)"
if [ -z "$BASE" ]; then BASE="$DOCKERD"; fi
# Always strip a previous Vibewatch TLS flag set. Host flags are only normalized
# when Vibewatch itself owns the listener; daemon.json-owned host listeners stay
# exactly where the administrator configured them.
BASE="$(printf '%s' "$BASE" | sed \
  -e "s# --tlsverify##g" \
  -e "s# --tlscacert=${TLS_DIR}/ca.pem##g" \
  -e "s# --tlscert=${TLS_DIR}/server-cert.pem##g" \
  -e "s# --tlskey=${TLS_DIR}/server-key.pem##g")"
REMOTE_HOST_ARG=""
if [ "$USE_DAEMON_HOSTS" -eq 0 ]; then
  BASE="$(printf '%s' "$BASE" | sed \
    -e "s# -H tcp://${BIND_IP}:2375##g" \
    -e "s# --host=tcp://${BIND_IP}:2375##g" \
    -e "s# -H=tcp://${BIND_IP}:2375##g" \
    -e "s# -H tcp://${BIND_IP}:2376##g" \
    -e "s# --host=tcp://${BIND_IP}:2376##g" \
    -e "s# -H=tcp://${BIND_IP}:2376##g")"
  # Adding an explicit TCP -H suppresses dockerd's implicit Unix socket. Keep a
  # local management endpoint when the original service had no explicit host flag.
  if ! printf '%s\n' "$BASE" | grep -Eq '(^|[[:space:]])(-H([=[:space:]])|--host([=[:space:]]))'; then
    BASE="${BASE} -H unix:///var/run/docker.sock"
  fi
  REMOTE_HOST_ARG=" -H tcp://${BIND_IP}:${SECURE_PORT}"
fi
rm -rf "$BACKUP"
install -d -m 0700 "$BACKUP"
if [ -f "$OVERRIDE" ]; then touch "$BACKUP/had-mtls"; cp -a "$OVERRIDE" "$BACKUP/mtls.conf"; fi
if [ -f "$LEGACY" ]; then touch "$BACKUP/had-tcp"; cp -a "$LEGACY" "$BACKUP/tcp.conf"; fi
if [ -d "$TLS_DIR" ]; then touch "$BACKUP/had-tlsdir"; cp -a "$TLS_DIR" "$BACKUP/tls"; fi
mkdir -p "$TLS_DIR" "$DROPIN_DIR"
if [ "$MANAGED_EXISTING" -eq 1 ]; then
  # Controller-loss/repair path: keep the current server identity and existing
  # trusted client CAs, then add the newly generated Vibewatch client CA.
  cat "$TLS_DIR/ca.pem" "$SRC_CA" > "$TLS_DIR/ca.pem.new"
  chmod 0644 "$TLS_DIR/ca.pem.new"
  mv "$TLS_DIR/ca.pem.new" "$TLS_DIR/ca.pem"
else
  install -m 0644 "$SRC_CA" "$TLS_DIR/ca.pem"
  install -m 0644 "$SRC_CERT" "$TLS_DIR/server-cert.pem"
  install -m 0600 "$SRC_KEY" "$TLS_DIR/server-key.pem"
fi
TMP="${OVERRIDE}.new"
cat > "$TMP" <<EOT
[Service]
ExecStart=
ExecStart=${BASE}${REMOTE_HOST_ARG} --tlsverify --tlscacert=${TLS_DIR}/ca.pem --tlscert=${TLS_DIR}/server-cert.pem --tlskey=${TLS_DIR}/server-key.pem
EOT
mv "$TMP" "$OVERRIDE"
# A successful secure migration must not leave the Vibewatch-created tcp/2375
# drop-in active, because its filename would otherwise override mTLS by order.
rm -f "$LEGACY"
systemctl daemon-reload
EFFECTIVE="$(systemctl cat docker.service | effective_execstart)"
EFFECTIVE_OK=1
if ! printf '%s\n' "$EFFECTIVE" | grep -Fq -- "--tlsverify"; then EFFECTIVE_OK=0; fi
if [ "$USE_DAEMON_HOSTS" -eq 0 ] && ! printf '%s\n' "$EFFECTIVE" | grep -Fq -- "tcp://${BIND_IP}:${SECURE_PORT}"; then EFFECTIVE_OK=0; fi
if [ "$EFFECTIVE_OK" -ne 1 ]; then
  rm -f "$OVERRIDE" "$LEGACY"
  rm -rf "$TLS_DIR"
  [ -f "$BACKUP/had-mtls" ] && cp -a "$BACKUP/mtls.conf" "$OVERRIDE" || true
  [ -f "$BACKUP/had-tcp" ] && cp -a "$BACKUP/tcp.conf" "$LEGACY" || true
  [ -f "$BACKUP/had-tlsdir" ] && cp -a "$BACKUP/tls" "$TLS_DIR" || true
  systemctl daemon-reload
  rm -rf "$BACKUP"
  fail "another docker.service drop-in overrides Vibewatch's staged mTLS ExecStart; no service restart was attempted" 51
fi
if ! systemctl restart docker.service; then
  echo "Docker rejected the staged mTLS configuration. Capturing diagnostics before rollback..."
  collect_diagnostics
  if local_rollback; then
    echo "Rollback successful: the previous Docker configuration was restored and Docker restarted."
  else
    echo "CRITICAL: rollback could not restart Docker. Manual recovery is required."
    collect_diagnostics
  fi
  rm -rf "$BACKUP"
  fail "Docker failed to restart with the staged mTLS configuration; see diagnostics above" 44
fi
sleep 2
if ! "$DOCKER" info >/dev/null 2>&1; then
  echo "Docker restarted but the local Docker API is unavailable. Capturing diagnostics before rollback..."
  collect_diagnostics
  if local_rollback; then
    echo "Rollback successful: the previous Docker configuration was restored and Docker restarted."
  else
    echo "CRITICAL: rollback could not restart Docker. Manual recovery is required."
    collect_diagnostics
  fi
  rm -rf "$BACKUP"
  fail "Docker did not pass local API validation; the staged mTLS change was rolled back" 45
fi
echo "Secure Docker endpoint staged at tcp://${BIND_IP}:${SECURE_PORT}"
echo "VIBEWATCH_SECURE_ENDPOINT=tls://${BIND_IP}:${SECURE_PORT}"
echo "Quick-setup transaction ${TXN} is ready for controller mTLS validation"
`

func secureEndpointFromOutput(output, ip string) string {
	fallback := "tls://" + strings.TrimSpace(ip) + ":2376"
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		const prefix = "VIBEWATCH_SECURE_ENDPOINT="
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		host := dockerEndpointHostForSetup(candidate)
		if host == strings.TrimSpace(ip) && strings.HasPrefix(strings.ToLower(candidate), "tls://") {
			return candidate
		}
	}
	return fallback
}

func dockerEndpointHostForSetup(endpoint string) string {
	raw := strings.TrimSpace(endpoint)
	for _, prefix := range []string{"tls://", "tcp://"} {
		if strings.HasPrefix(strings.ToLower(raw), prefix) {
			raw = raw[len(prefix):]
			break
		}
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return ""
	}
	return strings.Trim(host, "[]")
}

// ConfigureDockerMTLS installs a staged Vibewatch-managed mTLS listener on
// tcp/2376. Existing non-Vibewatch TLS configuration is detected and left
// untouched. The caller must CommitDockerSetup after controller reachability is
// verified, or RollbackDockerSetup when a later step fails.
func (c *Client) ConfigureDockerMTLS(ctx context.Context, ip, username, password string, port int, caPEM, serverCertPEM, serverKeyPEM string) (Result, error) {
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
	if strings.TrimSpace(caPEM) == "" || strings.TrimSpace(serverCertPEM) == "" || strings.TrimSpace(serverKeyPEM) == "" {
		return Result{}, fmt.Errorf("mTLS certificate material is incomplete")
	}
	if err := c.ensureSSHDir(); err != nil {
		return Result{}, err
	}
	asRoot, err := c.rootMode(ctx, ip, username, password, port)
	if err != nil {
		return Result{}, err
	}
	token, err := newTransactionToken()
	if err != nil {
		return Result{}, fmt.Errorf("generate quick-setup transaction: %w", err)
	}

	remoteBase := fmt.Sprintf("/tmp/vibewatch-mtls-%d", time.Now().UnixNano())
	uploads := []struct {
		path string
		data string
	}{
		{remoteBase + "-ca.pem", caPEM},
		{remoteBase + "-server-cert.pem", serverCertPEM},
		{remoteBase + "-server-key.pem", serverKeyPEM},
	}
	for _, up := range uploads {
		cmd := fmt.Sprintf("umask 077; cat > %s", shellQuote(up.path))
		if _, err := c.runSSH(ctx, ip, username, password, port, cmd, up.data); err != nil {
			for _, cleanup := range uploads {
				_, _ = c.runSSH(context.Background(), ip, username, password, port, "rm -f "+shellQuote(cleanup.path), "")
			}
			return Result{}, fmt.Errorf("upload mTLS certificate material failed: %w", err)
		}
	}

	payload := base64.StdEncoding.EncodeToString([]byte(configureDockerMTLSScript))
	remote := fmt.Sprintf(
		"echo %s | base64 -d | sh -s -- %s %s %s %s %s",
		payload,
		shellQuote(ip),
		shellQuote(uploads[0].path),
		shellQuote(uploads[1].path),
		shellQuote(uploads[2].path),
		shellQuote(token),
	)
	remote, stdin := privilegedCommand(remote, password, asRoot)
	output, err := c.runSSH(ctx, ip, username, password, port, remote, stdin)
	if err != nil {
		return Result{}, fmt.Errorf("remote secure Docker setup failed: %w", err)
	}
	effectiveCommand, effectiveStdin := privilegedCommand("cat /etc/docker/vibewatch-tls/ca.pem", password, asRoot)
	effectiveCA, caErr := c.runSSH(ctx, ip, username, password, port, effectiveCommand, effectiveStdin)
	if caErr != nil || strings.TrimSpace(effectiveCA) == "" {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _ = c.RollbackDockerSetup(rollbackCtx, ip, username, password, port, token, "secure")
		cancel()
		return Result{}, fmt.Errorf("secure Docker was staged, but its CA bundle could not be read back for controller trust; the remote change was rolled back")
	}
	return Result{IP: ip, Port: port, Username: username, Endpoint: secureEndpointFromOutput(output, ip), Output: strings.TrimSpace(output), CAPEM: strings.TrimSpace(effectiveCA), TransactionToken: token}, nil
}

const rollbackDockerSetupScript = `set -eu
MODE="$1"
TXN="$2"
BACKUP="/tmp/vibewatch-quicksetup-${TXN}"
DROPIN_DIR=/etc/systemd/system/docker.service.d
TCP=${DROPIN_DIR}/vibewatch-tcp.conf
MTLS=${DROPIN_DIR}/vibewatch-mtls.conf
TLS_DIR=/etc/docker/vibewatch-tls
case "$TXN" in *[!a-f0-9]*|'') echo "ERROR: invalid quick-setup transaction token"; exit 60 ;; esac
if [ ! -d "$BACKUP" ]; then
  echo "No staged quick-setup backup exists for ${TXN}; nothing to roll back"
  exit 0
fi
case "$MODE" in
  tcp)
    rm -f "$TCP"
    [ -f "$BACKUP/had-tcp" ] && cp -a "$BACKUP/tcp.conf" "$TCP" || true
    ;;
  secure)
    rm -f "$TCP" "$MTLS"
    rm -rf "$TLS_DIR"
    [ -f "$BACKUP/had-tcp" ] && cp -a "$BACKUP/tcp.conf" "$TCP" || true
    [ -f "$BACKUP/had-mtls" ] && cp -a "$BACKUP/mtls.conf" "$MTLS" || true
    [ -f "$BACKUP/had-tlsdir" ] && cp -a "$BACKUP/tls" "$TLS_DIR" || true
    ;;
  *) echo "ERROR: invalid quick-setup rollback mode"; exit 61 ;;
esac
systemctl daemon-reload
if ! systemctl restart docker.service; then
  echo "CRITICAL: previous files were restored but docker.service could not restart"
` + dockerDiagnosticScript + `
  exit 62
fi
rm -rf "$BACKUP"
echo "Quick-setup rollback successful; previous Docker configuration restored"
`

// RollbackDockerSetup restores the pre-Quick-Setup files for a staged remote
// transaction. It is used when controller reachability, credential persistence,
// or host persistence fails after dockerd itself accepted the new configuration.
func (c *Client) RollbackDockerSetup(ctx context.Context, ip, username, password string, port int, token, mode string) (string, error) {
	if err := validateTransactionToken(token); err != nil {
		return "", err
	}
	if mode != "tcp" && mode != "secure" {
		return "", fmt.Errorf("invalid quick-setup rollback mode")
	}
	asRoot, err := c.rootMode(ctx, ip, username, password, port)
	if err != nil {
		return "", err
	}
	payload := base64.StdEncoding.EncodeToString([]byte(rollbackDockerSetupScript))
	remote := fmt.Sprintf("echo %s | base64 -d | sh -s -- %s %s", payload, shellQuote(mode), shellQuote(token))
	remote, stdin := privilegedCommand(remote, password, asRoot)
	return c.runSSH(ctx, ip, username, password, port, remote, stdin)
}

// CommitDockerSetup removes the remote rollback snapshot after the controller
// has successfully reached the new endpoint and persisted the host state.
func (c *Client) CommitDockerSetup(ctx context.Context, ip, username, password string, port int, token string) (string, error) {
	if err := validateTransactionToken(token); err != nil {
		return "", err
	}
	asRoot, err := c.rootMode(ctx, ip, username, password, port)
	if err != nil {
		return "", err
	}
	command := fmt.Sprintf("rm -rf %s && echo %s", shellQuote("/tmp/vibewatch-quicksetup-"+token), shellQuote("Quick-setup transaction committed"))
	remote, stdin := privilegedCommand(command, password, asRoot)
	return c.runSSH(ctx, ip, username, password, port, remote, stdin)
}

// DockerSetupDiagnostics captures the small amount of service state needed to
// explain why a controller-side endpoint probe failed after a remote change.
func (c *Client) DockerSetupDiagnostics(ctx context.Context, ip, username, password string, port int) (string, error) {
	asRoot, err := c.rootMode(ctx, ip, username, password, port)
	if err != nil {
		return "", err
	}
	payload := base64.StdEncoding.EncodeToString([]byte(dockerDiagnosticScript))
	command := fmt.Sprintf("echo %s | base64 -d | sh", payload)
	remote, stdin := privilegedCommand(command, password, asRoot)
	return c.runSSH(ctx, ip, username, password, port, remote, stdin)
}
