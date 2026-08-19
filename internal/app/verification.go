package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
)

const (
	verificationStatusVerified      = "verified"
	verificationStatusFailed        = "failed"
	verificationStatusNotConfigured = "not_configured"
	verificationStatusRunning       = "running"
)

type VerificationCheck struct {
	ID              string `json:"id,omitempty"`
	Type            string `json:"type"` // http, https, tcp
	URL             string `json:"url,omitempty"`
	Host            string `json:"host,omitempty"`
	Port            int    `json:"port,omitempty"`
	ExpectedStatus  int    `json:"expected_status,omitempty"`
	ExpectedContent string `json:"expected_content,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
}

type VerificationCheckResult struct {
	Index      int    `json:"index"`
	Type       string `json:"type"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	HTTPStatus int    `json:"http_status,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type VerificationProfileView struct {
	HostID               int64               `json:"host_id"`
	ScopeType            string              `json:"scope_type"`
	ScopeKey             string              `json:"scope_key"`
	Enabled              bool                `json:"enabled"`
	StartDelaySeconds    int                 `json:"start_delay_seconds"`
	RetryCount           int                 `json:"retry_count"`
	RetryIntervalSeconds int                 `json:"retry_interval_seconds"`
	Checks               []VerificationCheck `json:"checks"`
	Inherited            bool                `json:"inherited,omitempty"`
	Configured           bool                `json:"configured"`
	UpdatedAt            string              `json:"updated_at,omitempty"`
}

type VerificationResult struct {
	Status      string                    `json:"status"`
	Configured  bool                      `json:"configured"`
	ScopeType   string                    `json:"scope_type,omitempty"`
	ScopeKey    string                    `json:"scope_key,omitempty"`
	StartedAt   string                    `json:"started_at,omitempty"`
	FinishedAt  string                    `json:"finished_at,omitempty"`
	DurationMS  int64                     `json:"duration_ms"`
	CheckResult []VerificationCheckResult `json:"checks"`
	Error       string                    `json:"error,omitempty"`
}

func verificationChecks(raw string) []VerificationCheck {
	var checks []VerificationCheck
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &checks)
	}
	if checks == nil {
		checks = []VerificationCheck{}
	}
	return checks
}

func verificationProfileView(x db.VerificationProfile, inherited bool) VerificationProfileView {
	return VerificationProfileView{HostID: x.HostID, ScopeType: x.ScopeType, ScopeKey: x.ScopeKey, Enabled: bool(x.Enabled), StartDelaySeconds: x.StartDelaySeconds, RetryCount: x.RetryCount, RetryIntervalSeconds: x.RetryIntervalSeconds, Checks: verificationChecks(x.ChecksJSON), Inherited: inherited, Configured: bool(x.Enabled) && len(verificationChecks(x.ChecksJSON)) > 0, UpdatedAt: x.UpdatedAt}
}

func verificationStateForProfile(profile VerificationProfileView, state db.VerificationState) db.VerificationState {
	if !profile.Configured {
		state.Status = verificationStatusNotConfigured
		state.Error = ""
		return state
	}
	// A newly created/edited profile invalidates an older successful result.
	// Keep the historical checked_at/details in storage, but present Pending
	// until this exact/effective profile has actually been executed again.
	if state.Status == "" || state.Status == verificationStatusNotConfigured || state.CheckedAt == "" {
		state.Status = "pending"
		return state
	}
	if profile.UpdatedAt != "" {
		pt, pe := time.Parse(time.RFC3339Nano, profile.UpdatedAt)
		st, se := time.Parse(time.RFC3339Nano, state.CheckedAt)
		if pe == nil && se == nil && pt.After(st) {
			state.Status = "pending"
			state.Error = ""
		}
	}
	return state
}

func (a *App) effectiveVerificationState(ctx context.Context, hostID int64, container string, profile VerificationProfileView) db.VerificationState {
	if profile.ScopeType == "stack" && strings.TrimSpace(profile.ScopeKey) != "" {
		x, err := a.Store.VerificationScopeState(ctx, hostID, "stack", profile.ScopeKey)
		if err != nil {
			x = db.VerificationScopeState{HostID: hostID, ScopeType: "stack", ScopeKey: profile.ScopeKey, Status: verificationStatusNotConfigured, DetailsJSON: "[]"}
		}
		state := db.VerificationState{HostID: hostID, ContainerName: container, ScopeType: "stack", ScopeKey: profile.ScopeKey, Status: x.Status, DetailsJSON: x.DetailsJSON, CheckedAt: x.CheckedAt, Error: x.Error}
		return verificationStateForProfile(profile, state)
	}
	state, err := a.Store.VerificationState(ctx, hostID, container)
	if err != nil {
		state = db.VerificationState{HostID: hostID, ContainerName: container, Status: verificationStatusNotConfigured, DetailsJSON: "[]"}
	}
	state.ScopeType = profile.ScopeType
	state.ScopeKey = profile.ScopeKey
	return verificationStateForProfile(profile, state)
}

func (a *App) saveVerificationStateForProfile(ctx context.Context, profile VerificationProfileView, hostID int64, container, status, detailsJSON, checkedAt, errText string) error {
	if profile.ScopeType == "stack" && strings.TrimSpace(profile.ScopeKey) != "" {
		return a.Store.SaveVerificationScopeState(ctx, db.VerificationScopeState{HostID: hostID, ScopeType: "stack", ScopeKey: profile.ScopeKey, Status: status, DetailsJSON: detailsJSON, CheckedAt: checkedAt, Error: errText})
	}
	return a.Store.SaveVerificationState(ctx, db.VerificationState{HostID: hostID, ContainerName: container, Status: status, DetailsJSON: detailsJSON, CheckedAt: checkedAt, Error: errText})
}

func (a *App) effectiveVerificationProfile(ctx context.Context, hostID int64, container string) (VerificationProfileView, error) {
	if x, err := a.Store.VerificationProfile(ctx, hostID, "container", container); err == nil {
		return verificationProfileView(x, false), nil
	}
	cur, err := a.inspectOne(ctx, hostID, container)
	if err != nil {
		return VerificationProfileView{HostID: hostID, ScopeType: "container", ScopeKey: container, RetryCount: 2, RetryIntervalSeconds: 3, Checks: []VerificationCheck{}}, nil
	}
	project := strings.TrimSpace(cur.Config.Labels["com.docker.compose.project"])
	if project != "" {
		if x, err := a.Store.VerificationProfile(ctx, hostID, "stack", project); err == nil {
			return verificationProfileView(x, true), nil
		}
	}
	return VerificationProfileView{HostID: hostID, ScopeType: "container", ScopeKey: container, Enabled: false, RetryCount: 2, RetryIntervalSeconds: 3, Checks: []VerificationCheck{}, Configured: false}, nil
}

func normalizeVerificationProfile(in VerificationProfileView) (VerificationProfileView, error) {
	in.ScopeType = strings.ToLower(strings.TrimSpace(in.ScopeType))
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	if in.ScopeType != "container" && in.ScopeType != "stack" {
		return in, fmt.Errorf("scope_type must be container or stack")
	}
	if in.ScopeKey == "" {
		return in, fmt.Errorf("scope_key is required")
	}
	if in.StartDelaySeconds < 0 || in.StartDelaySeconds > 600 {
		return in, fmt.Errorf("start_delay_seconds must be between 0 and 600")
	}
	if in.RetryCount < 0 || in.RetryCount > 20 {
		return in, fmt.Errorf("retry_count must be between 0 and 20")
	}
	if in.RetryIntervalSeconds < 0 || in.RetryIntervalSeconds > 300 {
		return in, fmt.Errorf("retry_interval_seconds must be between 0 and 300")
	}
	if len(in.Checks) > 20 {
		return in, fmt.Errorf("at most 20 verification checks are supported")
	}
	for i := range in.Checks {
		c := &in.Checks[i]
		c.Type = strings.ToLower(strings.TrimSpace(c.Type))
		if c.TimeoutSeconds <= 0 {
			c.TimeoutSeconds = 8
		}
		if c.TimeoutSeconds > 120 {
			return in, fmt.Errorf("check %d timeout_seconds must be <= 120", i+1)
		}
		switch c.Type {
		case "http", "https":
			u, err := url.Parse(strings.TrimSpace(c.URL))
			if err != nil || u.Host == "" || u.Scheme != c.Type {
				return in, fmt.Errorf("check %d requires a valid %s URL", i+1, c.Type)
			}
			if c.ExpectedStatus == 0 {
				c.ExpectedStatus = http.StatusOK
			}
		case "tcp":
			if strings.TrimSpace(c.Host) == "" || c.Port < 1 || c.Port > 65535 {
				return in, fmt.Errorf("check %d requires tcp host and port", i+1)
			}
		default:
			return in, fmt.Errorf("check %d type must be http, https or tcp", i+1)
		}
	}
	return in, nil
}

func (a *App) executeVerificationCheck(ctx context.Context, c VerificationCheck) (result VerificationCheckResult) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	result = VerificationCheckResult{Type: c.Type, Status: verificationStatusFailed}
	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch c.Type {
	case "tcp":
		result.Target = net.JoinHostPort(strings.TrimSpace(c.Host), strconv.Itoa(c.Port))
		d := net.Dialer{}
		conn, err := d.DialContext(checkCtx, "tcp", result.Target)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		_ = conn.Close()
		result.Status = verificationStatusVerified
		return result
	case "http", "https":
		result.Target = strings.TrimSpace(c.URL)
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, result.Target, nil)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		client := &http.Client{Timeout: timeout}
		resp, err := client.Do(req)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		defer resp.Body.Close()
		result.HTTPStatus = resp.StatusCode
		expected := c.ExpectedStatus
		if expected == 0 {
			expected = http.StatusOK
		}
		if resp.StatusCode != expected {
			result.Error = fmt.Sprintf("expected HTTP %d, got %d", expected, resp.StatusCode)
			return result
		}
		if want := c.ExpectedContent; want != "" {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			if readErr != nil {
				result.Error = readErr.Error()
				return result
			}
			if !strings.Contains(string(body), want) {
				result.Error = "expected response content was not found"
				return result
			}
		}
		result.Status = verificationStatusVerified
		return result
	default:
		result.Error = "unsupported verification check type"
		return result
	}
}

func (a *App) runCustomVerification(ctx context.Context, hostID int64, container, trigger, actor string, jobID int64) VerificationResult {
	if strings.TrimSpace(actor) == "" {
		actor = "system"
	}
	profile, _ := a.effectiveVerificationProfile(ctx, hostID, container)
	if !profile.Configured {
		result := VerificationResult{Status: verificationStatusNotConfigured, Configured: false, CheckResult: []VerificationCheckResult{}, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		bs, _ := json.Marshal(result.CheckResult)
		_ = a.Store.SaveVerificationState(context.Background(), db.VerificationState{HostID: hostID, ContainerName: container, Status: verificationStatusNotConfigured, DetailsJSON: string(bs), CheckedAt: result.FinishedAt})
		txID := int64(0)
		if jobID > 0 {
			if tx, e := a.Store.UpdateTransactionByJob(context.Background(), jobID); e == nil {
				txID = tx.ID
			}
		}
		_, _ = a.Store.AddVerificationHistory(context.Background(), db.VerificationHistory{HostID: hostID, ContainerName: container, Trigger: trigger, Actor: actor, JobID: jobID, TransactionID: txID, Status: result.Status, DurationMS: 0, DetailsJSON: string(bs)})
		return result
	}
	verificationStarted := time.Now()
	result := VerificationResult{Status: verificationStatusRunning, Configured: true, ScopeType: profile.ScopeType, ScopeKey: profile.ScopeKey, StartedAt: verificationStarted.UTC().Format(time.RFC3339Nano), CheckResult: make([]VerificationCheckResult, 0, len(profile.Checks))}
	if jobID > 0 {
		_ = a.Store.AddJobLog(ctx, jobID, "INFO", "verify", fmt.Sprintf("custom verification started (%s:%s, %d check(s))", profile.ScopeType, profile.ScopeKey, len(profile.Checks)))
	}
	_ = a.Store.Audit(context.Background(), actor, "verification.started", hostID, container, fmt.Sprintf("scope=%s:%s checks=%d", profile.ScopeType, profile.ScopeKey, len(profile.Checks)))
	_ = a.saveVerificationStateForProfile(context.Background(), profile, hostID, container, verificationStatusRunning, "[]", result.StartedAt, "")
	if profile.StartDelaySeconds > 0 {
		select {
		case <-ctx.Done():
			result.Status = verificationStatusFailed
			result.Error = ctx.Err().Error()
		case <-time.After(time.Duration(profile.StartDelaySeconds) * time.Second):
		}
	}
	for i, check := range profile.Checks {
		if result.Status == verificationStatusFailed {
			break
		}
		var last VerificationCheckResult
		attempts := profile.RetryCount + 1
		if attempts < 1 {
			attempts = 1
		}
		for attempt := 1; attempt <= attempts; attempt++ {
			last = a.executeVerificationCheck(ctx, check)
			last.Index = i
			last.Attempts = attempt
			if last.Status == verificationStatusVerified {
				break
			}
			if attempt < attempts && profile.RetryIntervalSeconds > 0 {
				select {
				case <-ctx.Done():
					last.Error = ctx.Err().Error()
					attempt = attempts
				case <-time.After(time.Duration(profile.RetryIntervalSeconds) * time.Second):
				}
			}
		}
		result.CheckResult = append(result.CheckResult, last)
		if last.Status != verificationStatusVerified {
			result.Status = verificationStatusFailed
			result.Error = fmt.Sprintf("check %d (%s %s) failed: %s", i+1, last.Type, last.Target, last.Error)
			break
		}
	}
	if result.Status != verificationStatusFailed {
		result.Status = verificationStatusVerified
	}
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.DurationMS = time.Since(verificationStarted).Milliseconds()
	bs, _ := json.Marshal(result.CheckResult)
	_ = a.saveVerificationStateForProfile(context.Background(), profile, hostID, container, result.Status, string(bs), result.FinishedAt, result.Error)
	txID := int64(0)
	if jobID > 0 {
		if tx, e := a.Store.UpdateTransactionByJob(context.Background(), jobID); e == nil {
			txID = tx.ID
		}
	}
	_, _ = a.Store.AddVerificationHistory(context.Background(), db.VerificationHistory{HostID: hostID, ContainerName: container, Trigger: trigger, Actor: actor, JobID: jobID, TransactionID: txID, Status: result.Status, ScopeType: profile.ScopeType, ScopeKey: profile.ScopeKey, DurationMS: result.DurationMS, DetailsJSON: string(bs), Error: result.Error})
	if result.Status == verificationStatusFailed {
		if jobID > 0 {
			_ = a.Store.AddJobLog(context.Background(), jobID, "ERROR", "verify", result.Error)
			_ = a.Store.AddJobLog(context.Background(), jobID, "INFO", "verify", fmt.Sprintf("configured readiness window exhausted: start_delay=%ds retry_count=%d retry_interval=%ds elapsed=%dms", profile.StartDelaySeconds, profile.RetryCount, profile.RetryIntervalSeconds, result.DurationMS))
			diagCtx, diagCancel := context.WithTimeout(context.Background(), 12*time.Second)
			if runtime, inspectErr := a.inspectOne(diagCtx, hostID, container); inspectErr == nil {
				health := "not_configured"
				if runtime.State.Health != nil && strings.TrimSpace(runtime.State.Health.Status) != "" {
					health = strings.TrimSpace(runtime.State.Health.Status)
				}
				_ = a.Store.AddJobLog(context.Background(), jobID, "INFO", "verify", fmt.Sprintf("failure runtime snapshot before rollback: image_id=%s state=%s running=%t restarting=%t exit_code=%d health=%s", runtime.Image, runtime.State.Status, runtime.State.Running, runtime.State.Restarting, runtime.State.ExitCode, health))
			} else if inspectErr != nil {
				_ = a.Store.AddJobLog(context.Background(), jobID, "WARN", "verify", "failure runtime snapshot unavailable before rollback: "+inspectErr.Error())
			}
			diagCancel()
		}
		_ = a.Store.Audit(context.Background(), actor, "verification.failed", hostID, container, result.Error)
		a.Logger.Warn("custom verification failed", "host_id", hostID, "container", container, "trigger", trigger, "error", result.Error)
		event := "manual_update"
		if strings.HasPrefix(trigger, "automation:") || strings.HasPrefix(trigger, "chain-auto:") {
			event = "auto"
		}
		// A standalone manual verification is diagnostic and did not update the
		// container, so do not emit update-failure notifications for it.
		if trigger != "manual-test" {
			go a.notifyHostUsers(hostID, event, container, "Verification failed · "+container, "Vibewatch could not verify the updated service. "+result.Error, "")
		}
	} else {
		if jobID > 0 {
			_ = a.Store.AddJobLog(context.Background(), jobID, "INFO", "verify", "custom verification passed")
		}
		_ = a.Store.Audit(context.Background(), actor, "verification.success", hostID, container, fmt.Sprintf("scope=%s:%s", profile.ScopeType, profile.ScopeKey))
	}
	return result
}

func (a *App) handleVerification(w http.ResponseWriter, r *http.Request, hostID int64) {
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	if container == "" {
		writeErr(w, 400, "container is required")
		return
	}
	if !a.hostAllowed(r, hostID) {
		writeErr(w, 403, "host access denied")
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := a.effectiveVerificationProfile(r.Context(), hostID, container)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		state := a.effectiveVerificationState(r.Context(), hostID, container, profile)
		writeJSON(w, 200, map[string]any{"profile": profile, "state": state})
	case http.MethodPost:
		if !a.requireAdmin(w, r) {
			return
		}
		result := a.runCustomVerification(r.Context(), hostID, container, "manual-test", a.actor(r), 0)
		writeJSON(w, http.StatusOK, result)
	case http.MethodPut:
		if !a.requireAdmin(w, r) {
			return
		}
		var in VerificationProfileView
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		if in.ScopeType == "" {
			in.ScopeType = "container"
		}
		if in.ScopeKey == "" {
			if in.ScopeType == "container" {
				in.ScopeKey = container
			} else if cur, err := a.inspectOne(r.Context(), hostID, container); err == nil {
				in.ScopeKey = strings.TrimSpace(cur.Config.Labels["com.docker.compose.project"])
			}
		}
		in.HostID = hostID
		in, err := normalizeVerificationProfile(in)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		bs, _ := json.Marshal(in.Checks)
		x := db.VerificationProfile{HostID: hostID, ScopeType: in.ScopeType, ScopeKey: in.ScopeKey, Enabled: db.Bool(in.Enabled), StartDelaySeconds: in.StartDelaySeconds, RetryCount: in.RetryCount, RetryIntervalSeconds: in.RetryIntervalSeconds, ChecksJSON: string(bs)}
		if err := a.Store.SaveVerificationProfile(r.Context(), x); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "verification.profile.save", hostID, container, fmt.Sprintf("scope=%s:%s checks=%d", in.ScopeType, in.ScopeKey, len(in.Checks)))
		profile, _ := a.effectiveVerificationProfile(r.Context(), hostID, container)
		writeJSON(w, 200, profile)
	case http.MethodDelete:
		if !a.requireAdmin(w, r) {
			return
		}
		scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type"))
		scopeKey := strings.TrimSpace(r.URL.Query().Get("scope_key"))
		if scopeType == "" || scopeKey == "" {
			if p, _ := a.effectiveVerificationProfile(r.Context(), hostID, container); p.Configured {
				scopeType, scopeKey = p.ScopeType, p.ScopeKey
			}
		}
		if scopeType == "" || scopeKey == "" {
			writeErr(w, 400, "scope_type and scope_key are required")
			return
		}
		if err := a.Store.DeleteVerificationProfile(r.Context(), hostID, scopeType, scopeKey); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if scopeType == "stack" {
			_ = a.Store.DeleteVerificationScopeState(r.Context(), hostID, scopeType, scopeKey)
		} else {
			_ = a.Store.SaveVerificationState(r.Context(), db.VerificationState{HostID: hostID, ContainerName: container, Status: verificationStatusNotConfigured, DetailsJSON: "[]"})
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "verification.profile.delete", hostID, container, fmt.Sprintf("scope=%s:%s", scopeType, scopeKey))
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}
