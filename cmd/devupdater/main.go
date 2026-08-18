package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/m9rph/vibewatch/internal/devupdate"
)

type runner struct {
	id, dataDir, workspace, controller, stage, backup, project string
	projectSource, dataSource, dbBackup                        string
	status                                                     devupdate.Status
	logMu                                                      sync.Mutex
}

func main() {
	r := &runner{
		id:            strings.TrimSpace(os.Getenv("VIBEWATCH_DEV_UPDATE_ID")),
		dataDir:       env("VIBEWATCH_DEV_UPDATE_DATA_DIR", "/data"),
		workspace:     env("VIBEWATCH_DEV_UPDATE_PROJECT_DIR", "/workspace"),
		controller:    env("VIBEWATCH_DEV_UPDATE_CONTROLLER", "vibewatch"),
		projectSource: strings.TrimSpace(os.Getenv("VIBEWATCH_DEV_UPDATE_PROJECT_SOURCE")),
		dataSource:    strings.TrimSpace(os.Getenv("VIBEWATCH_DEV_UPDATE_DATA_SOURCE")),
		dbBackup:      strings.TrimSpace(os.Getenv("VIBEWATCH_DEV_UPDATE_DB_BACKUP")),
	}
	if r.id == "" {
		fmt.Fprintln(os.Stderr, "VIBEWATCH_DEV_UPDATE_ID is required")
		os.Exit(2)
	}
	if r.projectSource == "" || r.dataSource == "" || r.dbBackup == "" {
		fmt.Fprintln(os.Stderr, "host project/data mount sources and database backup are required")
		os.Exit(2)
	}
	st, err := devupdate.ReadStatus(r.dataDir, r.id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	r.status = st
	p := devupdate.PathsFor(r.dataDir)
	r.stage = filepath.Join(p.Staged, r.id, "source")
	r.backup = filepath.Join(p.Backups, r.id, "source")
	if err := r.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func (r *runner) set(state, stage string, percent int, message string, err error) {
	r.status.State = state
	r.status.Stage = stage
	r.status.Percent = percent
	r.status.Message = message
	if r.status.StartedAt == "" && devupdate.IsActiveState(state) {
		r.status.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err != nil {
		r.status.Error = err.Error()
	} else if state != "failed" && state != "rolled_back" {
		r.status.Error = ""
	}
	if state == "completed" || state == "failed" || state == "rolled_back" {
		r.status.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_ = devupdate.WriteStatus(r.dataDir, r.status)
	if message != "" {
		_ = devupdate.AppendLog(r.dataDir, r.id, stage+": "+message)
	}
}

func (r *runner) run() error {
	r.set("preparing", "Preparing update", 5, "Development updater started outside the controller container.", nil)
	if err := r.discoverProject(); err != nil {
		return r.failBeforeSwitch(err)
	}
	r.set("backing_up", "Backing up current source", 12, "Creating a rollback copy of the current source tree.", nil)
	if err := devupdate.SnapshotSource(r.workspace, r.backup); err != nil {
		return r.failBeforeSwitch(fmt.Errorf("source backup failed: %w", err))
	}
	r.set("applying", "Applying package", 22, "Replacing source files while preserving .env, scripts/.env, runtime data and Git metadata.", nil)
	if err := devupdate.ApplySource(r.stage, r.workspace); err != nil {
		return r.rollbackSourceOnly(fmt.Errorf("apply source failed: %w", err))
	}

	r.set("building", "Building Vibewatch", 38, "Building the new controller image with Docker layer cache. The running controller stays online during this step.", nil)
	buildCtx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	err := r.compose(buildCtx, cancel, "build", "vibewatch")
	if err != nil {
		return r.rollbackSourceOnly(fmt.Errorf("Docker build failed: %w", err))
	}

	r.set("switching", "Switching controller", 78, "Build completed. Recreating only the Vibewatch controller; a short UI interruption is expected.", nil)
	switchCtx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	err = r.compose(switchCtx, cancel, "up", "-d", "--no-deps", "--force-recreate", "vibewatch")
	if err != nil {
		return r.rollbackAfterSwitch(fmt.Errorf("controller recreate failed: %w", err))
	}

	r.set("verifying", "Verifying new controller", 90, "Waiting for the recreated controller to become healthy.", nil)
	if err := r.waitHealthy(3 * time.Minute); err != nil {
		return r.rollbackAfterSwitch(fmt.Errorf("new controller did not become healthy: %w", err))
	}
	r.set("completed", "Development update complete", 100, "Vibewatch was rebuilt, recreated and passed its Docker health check.", nil)
	devupdate.CleanupOld(r.dataDir, 3)
	return nil
}

func (r *runner) discoverProject() error {
	if _, err := os.Stat(filepath.Join(r.workspace, "docker-compose.yml")); err != nil {
		return fmt.Errorf("project mount %s is unavailable: %w", r.workspace, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", `{{ index .Config.Labels "com.docker.compose.project" }}`, r.controller).CombinedOutput()
	if err == nil {
		r.project = strings.TrimSpace(string(out))
	}
	if r.project == "" {
		r.project = "vibewatch"
	}
	if _, err := r.envFile(); err != nil {
		return err
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	if out, err := exec.CommandContext(ctx2, "docker", "compose", "version").CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose plugin unavailable: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *runner) envFile() (string, error) {
	for _, candidate := range []string{filepath.Join(r.workspace, ".env"), filepath.Join(r.workspace, "scripts", ".env")} {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("no .env or scripts/.env found in the mounted project; refusing to rebuild without the existing environment")
}

func (r *runner) compose(ctx context.Context, cancel context.CancelFunc, args ...string) error {
	defer cancel()
	envFile, err := r.envFile()
	if err != nil {
		return err
	}
	base := []string{"compose", "-p", r.project, "--env-file", envFile, "-f", filepath.Join(r.workspace, "docker-compose.yml"), "-f", filepath.Join(r.workspace, "docker-compose.build.yml")}
	base = append(base, args...)
	return r.command(ctx, "docker", base...)
}

func (r *runner) command(ctx context.Context, name string, args ...string) error {
	_ = devupdate.AppendLog(r.dataDir, r.id, "$ "+name+" "+strings.Join(redactArgs(args), " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.workspace
	cmd.Env = append(os.Environ(),
		"VIBEWATCH_PROJECT_SOURCE="+r.projectSource,
		"VIBEWATCH_DATA_PATH="+r.dataSource,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go r.pipe(&wg, stdout)
	go r.pipe(&wg, stderr)
	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr != nil {
		return waitErr
	}
	return nil
}

func (r *runner) pipe(wg *sync.WaitGroup, rd io.Reader) {
	defer wg.Done()
	s := bufio.NewScanner(rd)
	buf := make([]byte, 64*1024)
	s.Buffer(buf, 2*1024*1024)
	for s.Scan() {
		r.logMu.Lock()
		_ = devupdate.AppendLog(r.dataDir, r.id, s.Text())
		r.logMu.Unlock()
	}
}
func redactArgs(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		if strings.Contains(strings.ToLower(out[i]), "password") || strings.Contains(strings.ToLower(out[i]), "secret") {
			out[i] = "***"
		}
	}
	return out
}

func (r *runner) stopController() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	err := r.command(ctx, "docker", "stop", "--time", "20", r.controller)
	cancel()
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	out, inspectErr := exec.CommandContext(inspectCtx, "docker", "inspect", "-f", `{{.State.Running}}`, r.controller).CombinedOutput()
	if inspectErr != nil {
		if err != nil && !strings.Contains(strings.ToLower(string(out)), "no such") {
			return fmt.Errorf("docker stop: %v; inspect: %w", err, inspectErr)
		}
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(string(out)), "true") {
		return errors.New("controller is still running")
	}
	return nil
}

func (r *runner) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", `{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}`, r.controller).CombinedOutput()
		cancel()
		if err == nil {
			last = strings.TrimSpace(string(out))
			if last == "healthy" {
				return nil
			}
			if last == "exited" || last == "dead" {
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	if last == "" {
		last = "unavailable"
	}
	return fmt.Errorf("controller health is %s", last)
}

func (r *runner) failBeforeSwitch(cause error) error {
	r.set("failed", "Update stopped", 100, "No controller switch was attempted. "+cause.Error(), cause)
	return cause
}
func (r *runner) rollbackSourceOnly(cause error) error {
	r.set("rolling_back", "Restoring previous source", 70, "The new source could not be built. The running controller was never replaced; restoring the previous source tree.", cause)
	if err := devupdate.ApplySource(r.backup, r.workspace); err != nil {
		joined := fmt.Errorf("%v; source rollback also failed: %w", cause, err)
		r.set("failed", "Manual recovery required", 100, joined.Error(), joined)
		return joined
	}
	r.set("failed", "Development update failed", 100, "Previous source restored. The original controller remained online.", cause)
	return cause
}
func (r *runner) rollbackAfterSwitch(cause error) error {
	r.set("rolling_back", "Rolling back development update", 82, "The new controller could not be verified. Restoring the previous database, source and controller.", cause)
	if err := r.stopController(); err != nil {
		return r.manualRecovery(cause, fmt.Errorf("stop failed controller before database restore: %w", err))
	}
	if err := devupdate.RestoreDatabaseBackup(r.dbBackup, r.dataDir); err != nil {
		return r.manualRecovery(cause, fmt.Errorf("restore pre-update database: %w", err))
	}
	if err := devupdate.ApplySource(r.backup, r.workspace); err != nil {
		return r.manualRecovery(cause, fmt.Errorf("restore previous source: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	if err := r.compose(ctx, cancel, "build", "vibewatch"); err != nil {
		return r.manualRecovery(cause, fmt.Errorf("rebuild previous image: %w", err))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 8*time.Minute)
	if err := r.compose(ctx2, cancel2, "up", "-d", "--no-deps", "--force-recreate", "vibewatch"); err != nil {
		return r.manualRecovery(cause, fmt.Errorf("recreate previous controller: %w", err))
	}
	if err := r.waitHealthy(3 * time.Minute); err != nil {
		return r.manualRecovery(cause, fmt.Errorf("previous controller health: %w", err))
	}
	r.set("rolled_back", "Previous version restored", 100, "Development update failed, but the previous source and healthy controller were restored automatically.", cause)
	return cause
}
func (r *runner) manualRecovery(cause, rollbackErr error) error {
	joined := fmt.Errorf("development update failed: %v; automatic rollback failed: %w", cause, rollbackErr)
	r.set("failed", "Manual recovery required", 100, joined.Error(), joined)
	return joined
}
