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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/m9rph/vibewatch/internal/devupdate"
)

type runner struct {
	id, dataDir, workspace, controller, stage, backup, project string
	projectSource, dataSource, dbBackup                        string
	previousImageID, previousImageRef, rollbackImageTag        string
	status                                                     devupdate.Status
	logMu                                                      sync.Mutex
}

var errDevUpdateCancelled = errors.New("development update cancelled")

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
	if devupdate.CancelRequested(r.dataDir, r.id) {
		r.status.CancelRequested = true
	}
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
	if state == "completed" || state == "failed" || state == "rolled_back" || state == "cancelled" || state == "recovery_required" {
		r.status.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if writeErr := devupdate.WriteStatus(r.dataDir, r.status); writeErr != nil {
		fmt.Fprintln(os.Stderr, "development update status write failed:", writeErr)
	}
	if message != "" {
		_ = devupdate.AppendLog(r.dataDir, r.id, stage+": "+message)
	}
}

func (r *runner) run() error {
	defer func() {
		if !devupdate.IsActiveState(r.status.State) {
			devupdate.ClearCancel(r.dataDir, r.id)
		}
	}()
	r.set("preparing", "Preparing update", 5, "Development updater started outside the controller container.", nil)
	if err := r.discoverProject(); err != nil {
		return r.failBeforeSwitch(err)
	}
	if err := r.preflightCapacity(); err != nil {
		return r.failBeforeSwitch(err)
	}
	if r.cancelRequested() {
		return r.failBeforeSwitch(errDevUpdateCancelled)
	}
	r.set("backing_up", "Backing up current source", 12, "Creating and validating a rollback copy of the current source tree.", nil)
	if err := devupdate.SnapshotSource(r.workspace, r.backup); err != nil {
		return r.failBeforeSwitch(fmt.Errorf("source backup failed: %w", err))
	}
	if backupVersion, err := devupdate.SourceTreeVersion(r.backup); err == nil {
		r.status.SourceBackupVersion = backupVersion
		if writeErr := devupdate.WriteStatus(r.dataDir, r.status); writeErr != nil {
			fmt.Fprintln(os.Stderr, "development update backup provenance write failed:", writeErr)
		}
	}
	if err := r.captureRollbackImage(); err != nil {
		return r.failBeforeSwitch(fmt.Errorf("pin current controller image for rollback: %w", err))
	}
	defer func() {
		// Keep the pinned previous controller image available while recovery is
		// unresolved. Manual/startup recovery may still need it.
		if r.status.State != "recovery_required" {
			r.cleanupRollbackImageTag()
		}
	}()
	if r.cancelRequested() {
		return r.failBeforeSwitch(errDevUpdateCancelled)
	}
	r.set("applying", "Applying package", 22, "Atomically overlaying source files while preserving .env, scripts/.env, runtime data and Git metadata.", nil)
	if err := devupdate.ApplySource(r.stage, r.workspace); err != nil {
		return r.rollbackSourceOnly(fmt.Errorf("apply source failed: %w", err))
	}
	if r.cancelRequested() {
		return r.rollbackSourceOnly(errDevUpdateCancelled)
	}

	r.set("building", "Building Vibewatch", 38, "Building the new controller image with Docker layer cache. The running controller stays online during this step.", nil)
	buildCtx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	err := r.compose(buildCtx, cancel, "build", "vibewatch")
	if err != nil {
		if errors.Is(err, errDevUpdateCancelled) {
			return r.rollbackSourceOnly(errDevUpdateCancelled)
		}
		return r.rollbackSourceOnly(fmt.Errorf("Docker build failed: %w", err))
	}
	if r.cancelRequested() {
		return r.rollbackSourceOnly(errDevUpdateCancelled)
	}
	if err := r.verifyPreSwitchRecoveryAssets(); err != nil {
		return r.rollbackSourceOnly(fmt.Errorf("pre-switch recovery asset verification failed: %w", err))
	}

	// From this point onward a safe cancel is advisory only. The controller
	// switch, health verification and any rollback must finish atomically.
	r.status.SwitchAttempted = true
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
	message := "Vibewatch was rebuilt, recreated and passed its Docker health check."
	if r.status.CancelRequested {
		message += " A late Safe Cancel request arrived after the atomic controller switch began, so the verified update was completed instead of interrupted."
	}
	r.set("completed", "Development update complete", 100, message, nil)
	devupdate.CleanupOld(r.dataDir, 3)
	return nil
}

func (r *runner) cancelRequested() bool { return devupdate.CancelRequested(r.dataDir, r.id) }

func (r *runner) preflightCapacity() error {
	workspace, err := devupdate.DiskUsage(r.workspace)
	if err != nil {
		return fmt.Errorf("inspect project filesystem capacity: %w", err)
	}
	data, err := devupdate.DiskUsage(r.dataDir)
	if err != nil {
		return fmt.Errorf("inspect data filesystem capacity: %w", err)
	}
	dockerFS, err := devupdate.DiskUsage("/vibewatch-docker-root")
	if err != nil {
		// Backward-compatible fallback for helpers launched by an older controller.
		dockerFS, err = r.dockerRootFilesystemUsage()
		if err != nil {
			return err
		}
	}
	_ = devupdate.AppendLog(r.dataDir, r.id, fmt.Sprintf("capacity preflight: workspace_free=%d data_free=%d docker_root=%s docker_free=%d", workspace.FreeBytes, data.FreeBytes, dockerFS.Path, dockerFS.FreeBytes))
	if workspace.FreeBytes < devupdate.MinWorkspaceFreeBytes || workspace.FreeInodes < devupdate.MinFreeInodes {
		return fmt.Errorf("project filesystem has insufficient recovery headroom: %d bytes free; need at least %d", workspace.FreeBytes, devupdate.MinWorkspaceFreeBytes)
	}
	if data.FreeBytes < devupdate.MinDataFreeBytes || data.FreeInodes < devupdate.MinFreeInodes {
		return fmt.Errorf("persistent data filesystem has insufficient update headroom: %d bytes free; need at least %d", data.FreeBytes, devupdate.MinDataFreeBytes)
	}
	if dockerFS.FreeBytes < devupdate.MinDockerBuildFreeBytes || dockerFS.FreeInodes < devupdate.MinFreeInodes {
		return fmt.Errorf("Docker build filesystem %s has insufficient free space: %d bytes free; need at least %d (free build cache or expand the filesystem)", dockerFS.Path, dockerFS.FreeBytes, devupdate.MinDockerBuildFreeBytes)
	}
	return nil
}

func (r *runner) dockerRootFilesystemUsage() (devupdate.DiskSpace, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rootOut, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.DockerRootDir}}").CombinedOutput()
	if err != nil {
		return devupdate.DiskSpace{}, fmt.Errorf("inspect Docker root: %s", strings.TrimSpace(string(rootOut)))
	}
	root := strings.TrimSpace(string(rootOut))
	imgOut, err := exec.CommandContext(ctx, "docker", "inspect", r.controller, "--format", "{{.Config.Image}}").CombinedOutput()
	if err != nil {
		return devupdate.DiskSpace{}, fmt.Errorf("inspect controller image for disk probe: %s", strings.TrimSpace(string(imgOut)))
	}
	image := strings.TrimSpace(string(imgOut))
	mount := "type=bind,src=" + root + ",dst=/vibewatch-docker-root,readonly"
	probe := func(flag string) ([]string, error) {
		out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--entrypoint", "/bin/df", "--mount", mount, image, flag, "/vibewatch-docker-root").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("probe Docker root capacity: %s", strings.TrimSpace(string(out)))
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 {
			return nil, fmt.Errorf("unexpected df output %q", string(out))
		}
		fields := strings.Fields(lines[len(lines)-1])
		if len(fields) < 6 {
			return nil, fmt.Errorf("unexpected df fields %q", lines[len(lines)-1])
		}
		return fields, nil
	}
	bf, err := probe("-Pk")
	if err != nil {
		return devupdate.DiskSpace{}, err
	}
	inf, err := probe("-Pi")
	if err != nil {
		return devupdate.DiskSpace{}, err
	}
	totalKB, err := strconv.ParseInt(bf[1], 10, 64)
	if err != nil {
		return devupdate.DiskSpace{}, err
	}
	freeKB, err := strconv.ParseInt(bf[3], 10, 64)
	if err != nil {
		return devupdate.DiskSpace{}, err
	}
	freeInodes, err := strconv.ParseUint(inf[3], 10, 64)
	if err != nil {
		return devupdate.DiskSpace{}, err
	}
	return devupdate.DiskSpace{Path: root, TotalBytes: totalKB * 1024, FreeBytes: freeKB * 1024, FreeInodes: freeInodes}, nil
}

func (r *runner) captureRollbackImage() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	idOut, err := exec.CommandContext(ctx, "docker", "inspect", r.controller, "--format", "{{.Image}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect current image id: %s", strings.TrimSpace(string(idOut)))
	}
	refOut, err := exec.CommandContext(ctx, "docker", "inspect", r.controller, "--format", "{{.Config.Image}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect current image ref: %s", strings.TrimSpace(string(refOut)))
	}
	r.previousImageID = strings.TrimSpace(string(idOut))
	r.previousImageRef = strings.TrimSpace(string(refOut))
	if r.previousImageID == "" || r.previousImageRef == "" {
		return errors.New("current controller image identity is incomplete")
	}
	safe := strings.Map(func(ch rune) rune {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			return ch
		}
		return '-'
	}, r.id)
	r.rollbackImageTag = "vibewatch-dev-rollback:" + safe
	out, err := exec.CommandContext(ctx, "docker", "image", "tag", r.previousImageID, r.rollbackImageTag).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tag rollback image: %s", strings.TrimSpace(string(out)))
	}
	r.status.PreviousImageID = r.previousImageID
	r.status.PreviousImageRef = r.previousImageRef
	return devupdate.WriteStatus(r.dataDir, r.status)
}

func (r *runner) cleanupRollbackImageTag() {
	if strings.TrimSpace(r.rollbackImageTag) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = exec.CommandContext(ctx, "docker", "image", "rm", r.rollbackImageTag).CombinedOutput()
}

func (r *runner) verifyPreSwitchRecoveryAssets() error {
	backupVersion, err := devupdate.SourceTreeVersion(r.backup)
	if err != nil {
		return err
	}
	if r.status.SourceBackupVersion != "" && backupVersion != r.status.SourceBackupVersion {
		return fmt.Errorf("source backup version changed from %s to %s", r.status.SourceBackupVersion, backupVersion)
	}
	workspaceVersion, err := devupdate.SourceTreeVersion(r.workspace)
	if err != nil {
		return err
	}
	if strings.TrimSpace(r.status.Version) != "" && workspaceVersion != strings.TrimSpace(r.status.Version) {
		return fmt.Errorf("workspace version %s does not match staged target %s", workspaceVersion, r.status.Version)
	}
	if st, err := os.Stat(r.dbBackup); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("database backup is missing or empty")
	}
	if strings.TrimSpace(r.rollbackImageTag) == "" {
		return errors.New("rollback image tag is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", r.rollbackImageTag).CombinedOutput(); err != nil {
		return fmt.Errorf("rollback image is unavailable: %s", strings.TrimSpace(string(out)))
	}
	if disk, err := devupdate.DiskUsage("/vibewatch-docker-root"); err == nil && disk.FreeBytes < devupdate.BuildAbortFreeBytes {
		return fmt.Errorf("Docker build filesystem fell below the recovery reserve after build: %d bytes free", disk.FreeBytes)
	}
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
	cmdCtx := ctx
	var stopWatch chan struct{}
	var localCancel context.CancelFunc
	pressure := make(chan error, 1)
	if !r.status.SwitchAttempted {
		cmdCtx, localCancel = context.WithCancel(ctx)
		stopWatch = make(chan struct{})
		go func() {
			t := time.NewTicker(750 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopWatch:
					return
				case <-cmdCtx.Done():
					return
				case <-t.C:
					if r.cancelRequested() {
						localCancel()
						return
					}
					if r.status.State == "building" {
						if disk, err := devupdate.DiskUsage("/vibewatch-docker-root"); err == nil && disk.FreeBytes < devupdate.BuildAbortFreeBytes {
							select {
							case pressure <- fmt.Errorf("Docker build stopped before disk exhaustion: %d bytes remain; recovery reserve is %d", disk.FreeBytes, devupdate.BuildAbortFreeBytes):
							default:
							}
							localCancel()
							return
						}
					}
				}
			}
		}()
	}
	if stopWatch != nil {
		defer close(stopWatch)
	}
	if localCancel != nil {
		defer localCancel()
	}
	cmd := exec.CommandContext(cmdCtx, name, args...)
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
		select {
		case pressureErr := <-pressure:
			return pressureErr
		default:
		}
		if !r.status.SwitchAttempted && r.cancelRequested() {
			return errDevUpdateCancelled
		}
		return waitErr
	}
	if !r.status.SwitchAttempted && r.cancelRequested() {
		return errDevUpdateCancelled
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
	if errors.Is(cause, errDevUpdateCancelled) {
		r.set("cancelled", "Development update cancelled", 100, "Safe cancel completed before any controller switch. The running controller was not replaced.", nil)
		return cause
	}
	r.set("failed", "Update stopped", 100, "No controller switch was attempted. "+cause.Error(), cause)
	return cause
}
func (r *runner) rollbackSourceOnly(cause error) error {
	r.set("rolling_back", "Restoring previous source", 70, "The new source could not be built. The running controller was never replaced; restoring the previous source tree.", cause)
	if err := devupdate.ApplySource(r.backup, r.workspace); err != nil {
		joined := fmt.Errorf("%v; source rollback also failed: %w", cause, err)
		r.set("recovery_required", "Manual recovery required", 100, joined.Error(), joined)
		return joined
	}
	if _, err := devupdate.SourceTreeVersion(r.workspace); err != nil {
		joined := fmt.Errorf("%v; restored source validation failed: %w", cause, err)
		r.set("recovery_required", "Manual recovery required", 100, joined.Error(), joined)
		return joined
	}
	if errors.Is(cause, errDevUpdateCancelled) {
		r.set("cancelled", "Development update cancelled", 100, "Safe cancel completed. The previous source tree was restored and the original controller remained online.", nil)
		return cause
	}
	r.set("failed", "Development update failed", 100, "Previous source restored and verified. The original controller remained online.", cause)
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
	if strings.TrimSpace(r.rollbackImageTag) == "" || strings.TrimSpace(r.previousImageRef) == "" {
		return r.manualRecovery(cause, errors.New("pinned previous controller image metadata is unavailable"))
	}
	retagCtx, retagCancel := context.WithTimeout(context.Background(), 20*time.Second)
	out, retagErr := exec.CommandContext(retagCtx, "docker", "image", "tag", r.rollbackImageTag, r.previousImageRef).CombinedOutput()
	retagCancel()
	if retagErr != nil {
		return r.manualRecovery(cause, fmt.Errorf("restore previous controller image tag: %s", strings.TrimSpace(string(out))))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 8*time.Minute)
	if err := r.compose(ctx2, cancel2, "up", "-d", "--no-deps", "--no-build", "--force-recreate", "vibewatch"); err != nil {
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
	r.set("recovery_required", "Manual recovery required", 100, joined.Error(), joined)
	return joined
}
