package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
)

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type updateChainView struct {
	db.UpdateChain
	Steps []db.UpdateChainStep `json:"steps"`
}

type ChainPreflightStepView struct {
	Position        int              `json:"position"`
	ContainerName   string           `json:"container_name"`
	UpdateAvailable bool             `json:"update_available"`
	Snoozed         bool             `json:"snoozed"`
	Status          string           `json:"status"`
	Warnings        int              `json:"warnings"`
	Blocked         int              `json:"blocked"`
	Summary         string           `json:"summary"`
	Checks          []PreflightCheck `json:"checks"`
	Error           string           `json:"error,omitempty"`
}

type ChainPreflightPlanView struct {
	ChainID   int64                    `json:"chain_id"`
	ChainName string                   `json:"chain_name"`
	HostID    int64                    `json:"host_id"`
	Status    string                   `json:"status"`
	Updates   int                      `json:"updates"`
	Warnings  int                      `json:"warnings"`
	Blocked   int                      `json:"blocked"`
	Steps     []ChainPreflightStepView `json:"steps"`
}

type ChainPreflightProgress struct {
	OperationID     string                   `json:"operation_id"`
	ChainID         int64                    `json:"chain_id"`
	ChainName       string                   `json:"chain_name"`
	HostID          int64                    `json:"host_id"`
	Status          string                   `json:"status"`
	Stage           string                   `json:"stage"`
	Message         string                   `json:"message"`
	CurrentPosition int                      `json:"current_position"`
	Total           int                      `json:"total"`
	Steps           []ChainPreflightStepView `json:"steps"`
	UpdatedAt       string                   `json:"updated_at"`
}

func (a *App) setChainPreflightProgress(progress ChainPreflightProgress) {
	id := strings.TrimSpace(progress.OperationID)
	if !validQuickSetupOperationID(id) {
		return
	}
	now := time.Now().UTC()
	progress.OperationID = id
	progress.UpdatedAt = now.Format(time.RFC3339Nano)
	progress.Steps = append([]ChainPreflightStepView(nil), progress.Steps...)
	a.chainPreflightMu.Lock()
	defer a.chainPreflightMu.Unlock()
	if a.chainPreflightOps == nil {
		a.chainPreflightOps = map[string]ChainPreflightProgress{}
	}
	for key, item := range a.chainPreflightOps {
		ts, err := time.Parse(time.RFC3339Nano, item.UpdatedAt)
		if err == nil && now.Sub(ts) > 20*time.Minute {
			delete(a.chainPreflightOps, key)
		}
	}
	a.chainPreflightOps[id] = progress
}

func (a *App) chainPreflightProgress(id string) (ChainPreflightProgress, bool) {
	a.chainPreflightMu.Lock()
	defer a.chainPreflightMu.Unlock()
	v, ok := a.chainPreflightOps[strings.TrimSpace(id)]
	v.Steps = append([]ChainPreflightStepView(nil), v.Steps...)
	return v, ok
}

type ChainManagementView struct {
	ChainID                int64  `json:"chain_id"`
	ChainName              string `json:"chain_name"`
	AutomationID           int64  `json:"automation_id"`
	ScopeType              string `json:"scope_type"`
	ScopeKey               string `json:"scope_key"`
	PolicyMode             string `json:"policy_mode"`
	AllowPreflightWarnings bool   `json:"allow_preflight_warnings"`
	LastRunAt              string `json:"last_run_at"`
	LastStatus             string `json:"last_status"`
}

func normalizeChainPolicyMode(v string) string {
	switch strings.TrimSpace(v) {
	case "manual", "auto", "ignore":
		return strings.TrimSpace(v)
	default:
		return "manual"
	}
}

func normalizeChainCurrentAction(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "restart", "recreate":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "skip"
	}
}

func (a *App) stackChainManagement(ctx context.Context, hostID int64) (map[string]ChainManagementView, error) {
	chains, err := a.Store.UpdateChains(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]ChainManagementView{}
	for _, c := range chains {
		if c.HostID != hostID || c.ScopeType != "stack" || strings.TrimSpace(c.ScopeKey) == "" {
			continue
		}
		out[c.ScopeKey] = ChainManagementView{ChainID: c.ID, ChainName: c.Name, AutomationID: c.AutomationID, ScopeType: c.ScopeType, ScopeKey: c.ScopeKey, PolicyMode: normalizeChainPolicyMode(c.PolicyMode), AllowPreflightWarnings: bool(c.AllowPreflightWarnings), LastRunAt: c.LastRunAt, LastStatus: c.LastStatus}
	}
	return out, nil
}

func (a *App) chainMemberManagement(ctx context.Context, hostID int64) (map[string]ChainManagementView, error) {
	chains, err := a.Store.UpdateChains(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]ChainManagementView{}
	for _, c := range chains {
		if c.HostID != hostID {
			continue
		}
		steps, err := a.Store.UpdateChainSteps(ctx, c.ID)
		if err != nil {
			continue
		}
		view := ChainManagementView{ChainID: c.ID, ChainName: c.Name, AutomationID: c.AutomationID, ScopeType: c.ScopeType, ScopeKey: c.ScopeKey, PolicyMode: normalizeChainPolicyMode(c.PolicyMode), AllowPreflightWarnings: bool(c.AllowPreflightWarnings), LastRunAt: c.LastRunAt, LastStatus: c.LastStatus}
		for _, st := range steps {
			name := strings.TrimSpace(st.ContainerName)
			if name != "" {
				out[name] = view
			}
		}
	}
	return out, nil
}

func (a *App) chainForMember(ctx context.Context, hostID int64, container string) (ChainManagementView, bool) {
	chains, err := a.Store.UpdateChains(ctx)
	if err != nil {
		return ChainManagementView{}, false
	}
	for _, c := range chains {
		if c.HostID != hostID {
			continue
		}
		steps, err := a.Store.UpdateChainSteps(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, st := range steps {
			if st.ContainerName == container {
				return ChainManagementView{ChainID: c.ID, ChainName: c.Name, AutomationID: c.AutomationID, ScopeType: c.ScopeType, ScopeKey: c.ScopeKey, PolicyMode: normalizeChainPolicyMode(c.PolicyMode), AllowPreflightWarnings: bool(c.AllowPreflightWarnings), LastRunAt: c.LastRunAt, LastStatus: c.LastStatus}, true
			}
		}
	}
	return ChainManagementView{}, false
}

func (a *App) validateStackChainMembers(ctx context.Context, hostID int64, stack string, steps []db.UpdateChainStep) error {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 30*time.Second, 90*time.Second))
	defer cancel()
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return fmt.Errorf("cannot validate stack membership: %w", err)
	}
	live := map[string]bool{}
	for _, c := range cs {
		if c.StackName == stack {
			if managed, _ := a.systemManagedContainer(c.Name); !managed {
				live[c.Name] = true
			}
		}
	}
	if len(live) == 0 {
		return fmt.Errorf("stack %s has no live containers on this host", stack)
	}
	configured := map[string]bool{}
	for _, st := range steps {
		configured[st.ContainerName] = true
	}
	missing, extra := []string{}, []string{}
	for name := range live {
		if !configured[name] {
			missing = append(missing, name)
		}
	}
	for name := range configured {
		if !live[name] {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("stack membership changed; sync the chain before running (missing=%s extra=%s)", strings.Join(missing, ","), strings.Join(extra, ","))
	}
	return nil
}

func chainReservationKey(hostID int64, container string) string {
	return fmt.Sprintf("%d:%s", hostID, strings.TrimSpace(container))
}

func (a *App) chainReservation(hostID int64, container string) (int64, bool) {
	a.chainMu.Lock()
	defer a.chainMu.Unlock()
	id, ok := a.chainReserved[chainReservationKey(hostID, container)]
	return id, ok
}

func (a *App) reserveChainMembers(ctx context.Context, chainID, hostID int64, steps []db.UpdateChainStep) error {
	a.chainMu.Lock()
	defer a.chainMu.Unlock()
	for _, st := range steps {
		key := chainReservationKey(hostID, st.ContainerName)
		if owner, exists := a.chainReserved[key]; exists {
			return fmt.Errorf("chain blocked: %s is already reserved by running update chain #%d", st.ContainerName, owner)
		}
		active, err := a.Store.HasActiveJob(ctx, hostID, st.ContainerName)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("chain blocked: %s already has a queued or running operation", st.ContainerName)
		}
		if recoveryRequired, err := a.Store.HasRecoveryRequiredTransaction(ctx, hostID, st.ContainerName); err != nil {
			return err
		} else if recoveryRequired {
			return fmt.Errorf("chain blocked: %s has an interrupted update transaction that requires recovery", st.ContainerName)
		}
	}
	for _, st := range steps {
		a.chainReserved[chainReservationKey(hostID, st.ContainerName)] = chainID
	}
	return nil
}

func (a *App) releaseChainMembers(chainID, hostID int64, steps []db.UpdateChainStep) {
	a.chainMu.Lock()
	defer a.chainMu.Unlock()
	for _, st := range steps {
		key := chainReservationKey(hostID, st.ContainerName)
		if owner, ok := a.chainReserved[key]; ok && owner == chainID {
			delete(a.chainReserved, key)
		}
	}
}

type updateChainInput struct {
	ID                     int64                `json:"id"`
	Name                   string               `json:"name"`
	HostID                 int64                `json:"host_id"`
	AutomationID           int64                `json:"automation_id"`
	ScopeType              string               `json:"scope_type"`
	ScopeKey               string               `json:"scope_key"`
	PolicyMode             string               `json:"policy_mode"`
	AllowPreflightWarnings bool                 `json:"allow_preflight_warnings"`
	StopOnFailure          bool                 `json:"stop_on_failure"`
	RollbackCompleted      bool                 `json:"rollback_completed"`
	Steps                  []db.UpdateChainStep `json:"steps"`
}

func (a *App) chainViews(ctx context.Context) ([]updateChainView, error) {
	chains, err := a.Store.UpdateChains(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]updateChainView, 0, len(chains))
	for _, c := range chains {
		steps, e := a.Store.UpdateChainSteps(ctx, c.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, updateChainView{UpdateChain: c, Steps: steps})
	}
	return out, nil
}

func normalizeChainInput(in updateChainInput) (updateChainInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, fmt.Errorf("chain name is required")
	}
	if in.HostID <= 0 {
		return in, fmt.Errorf("host is required")
	}
	in.ScopeType = strings.TrimSpace(in.ScopeType)
	if in.ScopeType == "" {
		in.ScopeType = "custom"
	}
	if in.ScopeType != "custom" && in.ScopeType != "stack" {
		return in, fmt.Errorf("scope_type must be custom or stack")
	}
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	if in.ScopeType == "stack" {
		if in.ScopeKey == "" {
			return in, fmt.Errorf("stack is required")
		}
	} else {
		in.ScopeKey = ""
	}
	// Every update chain owns the policy of its members. Legacy custom chains
	// stored as "inherit" normalize safely to Manual until explicitly changed.
	in.PolicyMode = normalizeChainPolicyMode(in.PolicyMode)
	if len(in.Steps) == 0 {
		return in, fmt.Errorf("at least one chain step is required")
	}
	if len(in.Steps) > 100 {
		return in, fmt.Errorf("at most 100 chain steps are supported")
	}
	seen := map[string]bool{}
	for i := range in.Steps {
		name := strings.TrimSpace(in.Steps[i].ContainerName)
		if name == "" {
			return in, fmt.Errorf("step %d container is required", i+1)
		}
		if seen[name] {
			return in, fmt.Errorf("container %s appears more than once in the chain", name)
		}
		seen[name] = true
		in.Steps[i].ContainerName = name
		in.Steps[i].Position = i + 1
		rawCurrentAction := strings.ToLower(strings.TrimSpace(in.Steps[i].CurrentAction))
		if rawCurrentAction != "" && rawCurrentAction != "skip" && rawCurrentAction != "restart" && rawCurrentAction != "recreate" {
			return in, fmt.Errorf("step %d current_action must be skip, restart or recreate", i+1)
		}
		in.Steps[i].CurrentAction = normalizeChainCurrentAction(rawCurrentAction)
		if in.Steps[i].WaitSeconds < 0 || in.Steps[i].WaitSeconds > 3600 {
			return in, fmt.Errorf("step %d wait_seconds must be between 0 and 3600", i+1)
		}
	}
	if in.AutomationID < 0 {
		return in, fmt.Errorf("automation_id must be zero or a valid automation id")
	}
	return in, nil
}

func (a *App) automationIncludesHost(ctx context.Context, automation db.Automation, hostID int64) bool {
	switch automation.TargetType {
	case "all":
		return true
	case "host":
		return automation.TargetID == hostID
	case "group":
		ids, err := a.Store.HostsForGroup(ctx, automation.TargetID)
		if err != nil {
			return false
		}
		for _, id := range ids {
			if id == hostID {
				return true
			}
		}
	}
	return false
}

func (a *App) handleUpdateChains(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, err := a.chainViews(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if !a.isAdmin(r) {
			filtered := rows[:0]
			for _, c := range rows {
				if a.hostAllowed(r, c.HostID) {
					filtered = append(filtered, c)
				}
			}
			rows = filtered
		}
		writeJSON(w, 200, rows)
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	var in updateChainInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var err error
	in, err = normalizeChainInput(in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := a.Store.Host(r.Context(), in.HostID); err != nil {
		writeErr(w, 400, "host not found")
		return
	}
	if in.AutomationID > 0 {
		automation, err := a.Store.Automation(r.Context(), in.AutomationID)
		if err != nil {
			writeErr(w, 400, "selected automation not found")
			return
		}
		if normalizeAutomationKind(automation.Kind) != "policy" {
			writeErr(w, 400, "selected automation is a cleanup run, not a policy run")
			return
		}
		if !a.automationIncludesHost(r.Context(), automation, in.HostID) {
			writeErr(w, 400, "selected automation does not target this Docker host")
			return
		}
	}
	if in.ScopeType == "stack" {
		chains, err := a.Store.UpdateChains(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		for _, other := range chains {
			if other.ID != in.ID && other.HostID == in.HostID && other.ScopeType == "stack" && other.ScopeKey == in.ScopeKey {
				writeErr(w, 409, fmt.Sprintf("stack %s is already managed by update chain %s", in.ScopeKey, other.Name))
				return
			}
		}
		if err := a.validateStackChainMembers(r.Context(), in.HostID, in.ScopeKey, in.Steps); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
	}
	// Refuse chains containing managed/system containers. Policies are evaluated
	// again at execution time because they can change after a chain is saved.
	for _, st := range in.Steps {
		if managed, _ := a.systemManagedContainer(st.ContainerName); managed {
			writeErr(w, 409, "Vibewatch system containers cannot be chain members")
			return
		}
	}
	x := db.UpdateChain{ID: in.ID, Name: in.Name, HostID: in.HostID, AutomationID: in.AutomationID, ScopeType: in.ScopeType, ScopeKey: in.ScopeKey, PolicyMode: in.PolicyMode, AllowPreflightWarnings: db.Bool(in.AllowPreflightWarnings), StopOnFailure: db.Bool(in.StopOnFailure), RollbackCompleted: db.Bool(in.RollbackCompleted)}
	id, err := a.Store.SaveUpdateChain(r.Context(), x, in.Steps)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "chain.save", in.HostID, "", fmt.Sprintf("chain=%d name=%s steps=%d", id, in.Name, len(in.Steps)))
	writeJSON(w, 200, map[string]any{"id": id})
}

func (a *App) handleUpdateChainRuns(w http.ResponseWriter, r *http.Request) {
	chainID, _ := strconv.ParseInt(r.URL.Query().Get("chain_id"), 10, 64)
	runs, err := a.Store.UpdateChainRuns(r.Context(), chainID, 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		if !a.hostAllowed(r, run.HostID) {
			continue
		}
		steps, _ := a.Store.UpdateChainRunSteps(r.Context(), run.ID)
		out = append(out, map[string]any{"run": run, "steps": steps})
	}
	writeJSON(w, 200, out)
}

func (a *App) previewUpdateChain(ctx context.Context, chainID int64, actor, operationID string) (ChainPreflightPlanView, error) {
	chain, err := a.Store.UpdateChain(ctx, chainID)
	if err != nil {
		return ChainPreflightPlanView{}, err
	}
	host, err := a.Store.Host(ctx, chain.HostID)
	if err != nil || !bool(host.Enabled) {
		if err == nil {
			err = fmt.Errorf("host is disabled")
		}
		return ChainPreflightPlanView{}, fmt.Errorf("chain blocked: Docker host unavailable: %w", err)
	}
	steps, err := a.Store.UpdateChainSteps(ctx, chainID)
	if err != nil || len(steps) == 0 {
		if err == nil {
			err = fmt.Errorf("chain has no steps")
		}
		return ChainPreflightPlanView{}, err
	}
	if chain.ScopeType == "stack" {
		if err := a.validateStackChainMembers(ctx, chain.HostID, chain.ScopeKey, steps); err != nil {
			return ChainPreflightPlanView{}, fmt.Errorf("chain blocked: %w", err)
		}
	}
	if normalizeChainPolicyMode(chain.PolicyMode) == "ignore" {
		return ChainPreflightPlanView{}, fmt.Errorf("chain blocked: chain policy is Excluded")
	}

	progress := ChainPreflightProgress{OperationID: operationID, ChainID: chain.ID, ChainName: chain.Name, HostID: chain.HostID, Status: "running", Stage: "prepare", Message: "Preparing the chain plan and checking member update state.", Total: len(steps), Steps: []ChainPreflightStepView{}}
	if operationID != "" {
		a.setChainPreflightProgress(progress)
	}
	plan := ChainPreflightPlanView{ChainID: chain.ID, ChainName: chain.Name, HostID: chain.HostID, Status: "ready", Steps: make([]ChainPreflightStepView, 0, len(steps))}
	for _, st := range steps {
		step := ChainPreflightStepView{Position: st.Position, ContainerName: st.ContainerName, Status: "checking", Checks: []PreflightCheck{}}
		if operationID != "" {
			progress.Stage = "member"
			progress.CurrentPosition = st.Position
			progress.Message = fmt.Sprintf("Checking %s (%d of %d).", st.ContainerName, st.Position, len(steps))
			a.setChainPreflightProgress(progress)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		_, _, checkErr := a.check(checkCtx, chain.HostID, st.ContainerName, fmt.Sprintf("chain-preview:%d", chain.ID))
		cancel()
		if checkErr != nil {
			step.Status = "blocked"
			step.Blocked = 1
			step.Summary = "Update check failed"
			step.Error = checkErr.Error()
			plan.Blocked++
			plan.Status = "blocked"
			plan.Steps = append(plan.Steps, step)
			if operationID != "" {
				progress.Steps = append([]ChainPreflightStepView(nil), plan.Steps...)
				progress.Message = fmt.Sprintf("%s is blocked because its update state could not be checked.", st.ContainerName)
				a.setChainPreflightProgress(progress)
			}
			continue
		}
		cache, _ := a.Store.Cache(ctx, chain.HostID, st.ContainerName)
		step.Snoozed = cacheHasSnoozedUpdate(cache)
		step.UpdateAvailable = bool(cache.UpdateAvailable) && !step.Snoozed
		if step.Snoozed {
			step.Status = "snoozed"
			step.Summary = "Available update is snoozed"
			plan.Steps = append(plan.Steps, step)
			if operationID != "" {
				progress.Steps = append([]ChainPreflightStepView(nil), plan.Steps...)
				a.setChainPreflightProgress(progress)
			}
			continue
		}
		if !step.UpdateAvailable {
			step.Status = "current"
			step.Summary = "No update available"
			plan.Steps = append(plan.Steps, step)
			if operationID != "" {
				progress.Steps = append([]ChainPreflightStepView(nil), plan.Steps...)
				a.setChainPreflightProgress(progress)
			}
			continue
		}

		plan.Updates++
		if operationID != "" {
			progress.Message = fmt.Sprintf("Running safety checks for %s.", st.ContainerName)
			a.setChainPreflightProgress(progress)
		}
		preflight, _ := a.runUpdatePreflight(ctx, updateRequest{HostID: chain.HostID, Container: st.ContainerName, Trigger: fmt.Sprintf("chain-preview:%d", chain.ID), Actor: actor}, false)
		step.Status = preflight.Status
		step.Warnings = preflight.Warnings
		step.Blocked = preflight.Blocked
		step.Summary = preflight.Summary
		step.Checks = preflight.Checks
		plan.Warnings += preflight.Warnings
		plan.Blocked += preflight.Blocked
		if preflight.Status == "blocked" {
			plan.Status = "blocked"
		} else if preflight.Status == "ready_with_warnings" && plan.Status != "blocked" {
			plan.Status = "ready_with_warnings"
		}
		plan.Steps = append(plan.Steps, step)
		if operationID != "" {
			progress.Steps = append([]ChainPreflightStepView(nil), plan.Steps...)
			progress.Message = fmt.Sprintf("Preflight finished for %s.", st.ContainerName)
			a.setChainPreflightProgress(progress)
		}
	}
	if plan.Updates == 0 && plan.Blocked == 0 {
		plan.Status = "no_updates"
	}
	if operationID != "" {
		progress.Status = "completed"
		progress.Stage = "decision"
		progress.CurrentPosition = len(steps)
		progress.Steps = append([]ChainPreflightStepView(nil), plan.Steps...)
		progress.Message = fmt.Sprintf("Chain Preflight finished: %s.", strings.ReplaceAll(plan.Status, "_", " "))
		a.setChainPreflightProgress(progress)
	}
	return plan, nil
}

func (a *App) handleUpdateChainSubroutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/update-chains/"), "/")
	parts := strings.Split(path, "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id <= 0 {
		writeErr(w, 400, "invalid chain id")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		chain, err := a.Store.UpdateChain(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "update chain not found")
			return
		}
		activeRuns, err := a.Store.ActiveUpdateChainRuns(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		for _, run := range activeRuns {
			if run.ChainID == id {
				writeErr(w, 409, fmt.Sprintf("update chain %s has an active %s run and cannot be deleted yet", chain.Name, run.Status))
				return
			}
		}
		if err := a.Store.DeleteUpdateChain(r.Context(), id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "chain.delete", chain.HostID, "", fmt.Sprintf("chain=%d name=%s", id, chain.Name))
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if len(parts) == 3 && parts[1] == "preflight" && parts[2] == "progress" && r.Method == http.MethodGet {
		opID := strings.TrimSpace(r.URL.Query().Get("id"))
		if !validQuickSetupOperationID(opID) {
			writeErr(w, 400, "invalid chain preflight operation id")
			return
		}
		progress, ok := a.chainPreflightProgress(opID)
		if !ok || progress.ChainID != id {
			writeErr(w, 404, "chain preflight operation not found")
			return
		}
		writeJSON(w, 200, progress)
		return
	}
	if len(parts) == 2 && parts[1] == "preflight" && r.Method == http.MethodPost {
		opID := strings.TrimSpace(r.URL.Query().Get("operation_id"))
		if opID != "" && !validQuickSetupOperationID(opID) {
			writeErr(w, 400, "invalid chain preflight operation id")
			return
		}
		plan, err := a.previewUpdateChain(r.Context(), id, a.actor(r), opID)
		if err != nil {
			if opID != "" {
				if progress, ok := a.chainPreflightProgress(opID); ok {
					progress.Status = "failed"
					progress.Stage = "decision"
					progress.Message = err.Error()
					a.setChainPreflightProgress(progress)
				}
			}
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, plan)
		return
	}
	if len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost {
		jobID, runID, err := a.startUpdateChain(r.Context(), id, "manual", a.actor(r))
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "run_id": runID, "status": "queued"})
		return
	}
	writeErr(w, 404, "not found")
}

func (a *App) startUpdateChain(ctx context.Context, chainID int64, trigger, actor string) (int64, int64, error) {
	chain, err := a.Store.UpdateChain(ctx, chainID)
	if err != nil {
		return 0, 0, err
	}
	host, err := a.Store.Host(ctx, chain.HostID)
	if err != nil || !bool(host.Enabled) {
		if err == nil {
			err = fmt.Errorf("host is disabled")
		}
		return 0, 0, fmt.Errorf("chain blocked: Docker host unavailable: %w", err)
	}
	if activeRuns, activeErr := a.Store.ActiveUpdateChainRuns(ctx); activeErr == nil {
		for _, activeRun := range activeRuns {
			if activeRun.ChainID == chainID {
				return 0, 0, fmt.Errorf("chain blocked: previous run #%d is %s and must be reconciled before a new run", activeRun.ID, activeRun.Status)
			}
		}
	}
	steps, err := a.Store.UpdateChainSteps(ctx, chainID)
	if err != nil || len(steps) == 0 {
		if err == nil {
			err = fmt.Errorf("chain has no steps")
		}
		return 0, 0, err
	}
	// Every chain owns the policy of its members. Stack chains additionally
	// validate that every configured member still belongs to the selected stack.
	if chain.ScopeType == "stack" {
		if err := a.validateStackChainMembers(ctx, chain.HostID, chain.ScopeKey, steps); err != nil {
			return 0, 0, fmt.Errorf("chain blocked: %w", err)
		}
	}
	mode := normalizeChainPolicyMode(chain.PolicyMode)
	if mode == "ignore" {
		return 0, 0, fmt.Errorf("chain blocked: chain policy is Excluded")
	}
	if trigger == "automatic" && mode != "auto" {
		return 0, 0, fmt.Errorf("chain blocked: chain policy is not Auto Update")
	}
	if err := a.reserveChainMembers(ctx, chain.ID, chain.HostID, steps); err != nil {
		return 0, 0, err
	}
	releaseReservation := true
	defer func() {
		if releaseReservation {
			a.releaseChainMembers(chain.ID, chain.HostID, steps)
		}
	}()
	if actor == "" {
		actor = "system"
	}
	jobID, err := a.Store.CreateJob(ctx, "chain", "chain-"+trigger, chain.HostID, chain.Name, "queued")
	if err != nil {
		return 0, 0, err
	}
	runID, err := a.Store.CreateUpdateChainRun(ctx, db.UpdateChainRun{ChainID: chain.ID, ChainName: chain.Name, HostID: chain.HostID, JobID: jobID, Trigger: trigger, Actor: actor, Status: "running"})
	if err != nil {
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		return 0, 0, err
	}
	_ = a.Store.Audit(ctx, actor, "chain.queue", chain.HostID, "", fmt.Sprintf("chain=%d run=%d", chain.ID, runID))
	releaseReservation = false
	go a.executeUpdateChain(chain, steps, jobID, runID, trigger, actor)
	return jobID, runID, nil
}

func (a *App) waitForJob(ctx context.Context, id int64) (db.Job, error) {
	for {
		job, err := a.Store.Job(ctx, id)
		if err != nil {
			return db.Job{}, err
		}
		if job.Status == "success" || job.Status == "failed" || job.Status == "cancelled" || job.Status == "skipped" {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return db.Job{}, ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
}

func criticalChainFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Stop-on-failure may be disabled for an intentionally tolerant service
	// group, but safety/control-plane failures must never let the transaction
	// continue into later members. Keep this classification tied to errors
	// emitted by the shared update/check pipeline rather than application names.
	for _, marker := range []string{
		"preflight", "verification", "rollback", "restore", "dependency",
		"registry", "manifest", "architecture", "docker host", "worker",
		"connection", "context deadline", "timed out", "timeout",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

type completedChainAction struct {
	Container      string
	Kind           string
	RestorePointID int64
}

type chainStepAvailability struct {
	UpdateAvailable bool
	Snoozed         bool
}

func (a *App) rollbackChainRestorePoint(ctx context.Context, runID, chainJobID int64, container string, rp db.RestorePoint, actor, reason string) error {
	if rp.ID <= 0 {
		return fmt.Errorf("restore point unavailable")
	}
	rbJob, err := a.Store.CreateJob(ctx, "rollback", fmt.Sprintf("chain:%d:%s", runID, reason), rp.HostID, container, "running")
	if err != nil {
		return err
	}
	_ = a.Store.StartJob(ctx, rbJob)
	if err := a.executeRestorePointRollback(rbJob, rp, actor, fmt.Sprintf("chain:%d:%s", runID, reason)); err != nil {
		_ = a.Store.AddJobLog(context.Background(), chainJobID, "ERROR", "chain", fmt.Sprintf("rollback of %s failed: %v", container, err))
		return err
	}
	_ = a.Store.AddJobLog(context.Background(), chainJobID, "INFO", "chain", "rolled back completed step: "+container)
	return nil
}

func (a *App) rollbackCompletedChainMembers(ctx context.Context, runID, chainJobID int64, completed []completedChainAction, hostID int64, actor string) []string {
	failed := []string{}
	for i := len(completed) - 1; i >= 0; i-- {
		action := completed[i]
		container := action.Container
		// A restart changes no container filesystem/configuration and therefore has
		// nothing meaningful to roll back. Recreates, however, have their own
		// retained full restore point and must participate in rollback-completed.
		if action.Kind == "restart" {
			continue
		}
		var rp db.RestorePoint
		if action.RestorePointID > 0 {
			rp, _ = a.Store.RestorePoint(ctx, action.RestorePointID)
		} else {
			rows, err := a.Store.UpdateHistory(ctx, 50, hostID, container)
			if err != nil {
				failed = append(failed, container+": history unavailable")
				continue
			}
			manualTrigger := fmt.Sprintf("chain:%d", runID)
			autoTrigger := fmt.Sprintf("chain-auto:%d", runID)
			for _, h := range rows {
				if h.Action == "update" && h.Status == "success" && (h.Trigger == manualTrigger || h.Trigger == autoTrigger) && h.RestorePointID > 0 {
					rp, _ = a.Store.RestorePoint(ctx, h.RestorePointID)
					break
				}
			}
		}
		if rp.ID == 0 {
			failed = append(failed, container+": restore point unavailable")
			continue
		}
		if err := a.rollbackChainRestorePoint(ctx, runID, chainJobID, container, rp, actor, "rollback-completed"); err != nil {
			failed = append(failed, container+": "+err.Error())
		}
	}
	return failed
}

func waitChainStep(ctx context.Context, d int) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(d) * time.Second):
		return nil
	}
}

func (a *App) verifyChainLifecycleAction(ctx context.Context, hostID int64, container, trigger, actor string, jobID int64, shouldRun bool) error {
	if shouldRun {
		if err := a.verifyUpdatedContainer(ctx, hostID, container); err != nil {
			return fmt.Errorf("Docker health verification failed: %w", err)
		}
		verification := a.runCustomVerification(ctx, hostID, container, trigger, actor, jobID)
		if verification.Status == verificationStatusFailed {
			return fmt.Errorf("custom verification failed: %s", verification.Error)
		}
		return nil
	}
	cur, err := a.inspectOne(ctx, hostID, container)
	if err != nil {
		return err
	}
	if cur.State.Running || cur.State.Restarting {
		return fmt.Errorf("container should remain stopped but is running")
	}
	return nil
}

func (a *App) restartCurrentChainMember(ctx context.Context, runID, chainJobID, hostID int64, container, actor string) (bool, error) {
	leaseKey, leaseOwner, leaseErr := a.acquireOperationLease(ctx, chainJobID, hostID, container, "chain-restart")
	if leaseErr != nil {
		return false, leaseErr
	}
	stopLeaseHeartbeat := a.startLeaseHeartbeat(ctx, leaseKey, leaseOwner, 0)
	defer stopLeaseHeartbeat()
	defer a.Store.ReleaseOperationLease(context.Background(), leaseKey, leaseOwner)
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return false, err
	}
	cur, err := a.inspectOne(ctx, hostID, container)
	if err != nil {
		return false, err
	}
	if !cur.State.Running && !cur.State.Restarting {
		_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", container+" is current but stopped; Restart if current preserves the stopped state")
		return false, nil
	}
	_ = a.Store.Audit(ctx, actor, "chain.step.restart.started", hostID, container, fmt.Sprintf("run=%d", runID))
	if _, err := a.Docker.Run(ctx, h.Endpoint, "restart", "-t", "10", container); err != nil {
		_ = a.Store.Audit(context.Background(), actor, "chain.step.restart.failed", hostID, container, fmt.Sprintf("run=%d error=%s", runID, err.Error()))
		return true, err
	}
	trigger := fmt.Sprintf("chain-restart:%d", runID)
	if err := a.verifyChainLifecycleAction(ctx, hostID, container, trigger, actor, chainJobID, true); err != nil {
		_ = a.Store.Audit(context.Background(), actor, "chain.step.restart.failed", hostID, container, fmt.Sprintf("run=%d error=%s", runID, err.Error()))
		return true, err
	}
	if err := a.captureCurrentConfigDriftBaseline(ctx, hostID, container, "post-chain-restart"); err != nil {
		_ = a.Store.AddJobLog(ctx, chainJobID, "WARN", "config-drift", "Could not refresh drift baseline after restart: "+err.Error())
	}
	_ = a.Store.Audit(ctx, actor, "chain.step.restart.success", hostID, container, fmt.Sprintf("run=%d", runID))
	return true, nil
}

func (a *App) recreateCurrentChainMember(ctx context.Context, runID, chainJobID, hostID int64, container, actor string, skipDataCapture bool) (db.RestorePoint, bool, error) {
	leaseKey, leaseOwner, leaseErr := a.acquireOperationLease(ctx, chainJobID, hostID, container, "chain-recreate")
	if leaseErr != nil {
		return db.RestorePoint{}, false, leaseErr
	}
	stopLeaseHeartbeat := a.startLeaseHeartbeat(ctx, leaseKey, leaseOwner, 0)
	leaseReleased := false
	releaseLease := func() {
		if leaseReleased {
			return
		}
		leaseReleased = true
		stopLeaseHeartbeat()
		_ = a.Store.ReleaseOperationLease(context.Background(), leaseKey, leaseOwner)
	}
	defer releaseLease()
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return db.RestorePoint{}, false, err
	}
	target, deps, err := a.discoverNetworkNamespaceDependents(ctx, hostID, container)
	if err != nil {
		return db.RestorePoint{}, false, fmt.Errorf("dependency discovery before recreate: %w", err)
	}
	if strings.TrimSpace(target.Config.Labels["com.docker.swarm.service.name"]) != "" {
		return db.RestorePoint{}, false, fmt.Errorf("Recreate if current is not supported for Docker Swarm services")
	}
	wasRunning := target.State.Running || target.State.Restarting
	reason := fmt.Sprintf("chain-%d-before-current-recreate", runID)
	snapshotCtx, snapshotCancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 2*time.Minute, 4*time.Minute))
	snap, err := a.createSnapshotForContainer(snapshotCtx, hostID, container, reason)
	snapshotCancel()
	if err != nil {
		return db.RestorePoint{}, false, fmt.Errorf("config snapshot before recreate: %w", err)
	}
	restoreCtx, restoreCancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 12*time.Minute, 20*time.Minute))
	capture, err := a.createRestorePointForSnapshotWithOptions(restoreCtx, hostID, container, snap, reason, fmt.Sprintf("chain-recreate:%d", runID), deps, restorePointCaptureOptions{CaptureData: !skipDataCapture, DeferWriterRestart: map[string]bool{container: true}})
	restoreCancel()
	rp := capture.RestorePoint
	deferredReleased := false
	releaseDeferred := func() error {
		if deferredReleased || len(capture.DeferredWriters) == 0 {
			deferredReleased = true
			return nil
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
		defer releaseCancel()
		err := a.ensureDataWritersRunning(releaseCtx, hostID, capture.DeferredWriters)
		if err == nil {
			deferredReleased = true
		}
		return err
	}
	defer func() { _ = releaseDeferred() }()
	if err != nil {
		return rp, false, fmt.Errorf("full restore point before recreate: %w", err)
	}
	if rp.ID <= 0 || !bool(rp.WritableLayer) || strings.TrimSpace(rp.ImageRef) == "" {
		return rp, false, fmt.Errorf("full restore point is not available for safe recreate")
	}
	_ = a.Store.Audit(ctx, actor, "chain.step.recreate.started", hostID, container, fmt.Sprintf("run=%d restore_point=%d", runID, rp.ID))
	if len(deps) > 0 {
		stopDependentsBestEffort(ctx, a, h.Endpoint, deps)
	}
	imageID := strings.TrimSpace(target.Image)
	originalRef := strings.TrimSpace(target.Config.Image)
	if imageID == "" && originalRef == "" {
		return rp, false, fmt.Errorf("current image reference is unavailable")
	}
	imageRef := a.prepareRuntimeRestoreRef(ctx, h.Endpoint, imageID, originalRef)
	if strings.TrimSpace(imageRef) == "" {
		imageRef = firstNonEmpty(imageID, originalRef)
	}
	if err := a.recreateContainerRuntime(ctx, h.Endpoint, target, imageRef, wasRunning, ""); err != nil {
		releaseLease()
		rbErr := a.rollbackChainRestorePoint(context.Background(), runID, chainJobID, container, rp, actor, "failed-current-recreate")
		if rbErr != nil {
			return rp, true, fmt.Errorf("recreate failed: %v; automatic restore also failed: %v", err, rbErr)
		}
		return rp, true, fmt.Errorf("recreate failed: %v; original container restored", err)
	}
	if err := releaseDeferred(); err != nil {
		releaseLease()
		rbErr := a.rollbackChainRestorePoint(context.Background(), runID, chainJobID, container, rp, actor, "failed-current-recreate-start")
		if rbErr != nil {
			return rp, true, fmt.Errorf("recreated data writer could not start: %v; automatic restore also failed: %v", err, rbErr)
		}
		return rp, true, fmt.Errorf("recreated data writer could not start: %v; original container restored", err)
	}
	parentAfter, inspectErr := a.inspectOne(ctx, hostID, container)
	if inspectErr != nil {
		releaseLease()
		rbErr := a.rollbackChainRestorePoint(context.Background(), runID, chainJobID, container, rp, actor, "failed-current-recreate")
		if rbErr != nil {
			return rp, true, fmt.Errorf("recreated container cannot be inspected: %v; automatic restore also failed: %v", inspectErr, rbErr)
		}
		return rp, true, fmt.Errorf("recreated container cannot be inspected: %v; original container restored", inspectErr)
	}
	if len(deps) > 0 && strings.TrimSpace(parentAfter.ID) != strings.TrimSpace(target.ID) {
		if depErr := a.recreateNetworkNamespaceDependents(ctx, chainJobID, hostID, container, parentAfter.ID, deps); depErr != nil {
			releaseLease()
			rbErr := a.rollbackChainRestorePoint(context.Background(), runID, chainJobID, container, rp, actor, "failed-current-recreate")
			if rbErr != nil {
				return rp, true, fmt.Errorf("dependent recreation failed: %v; automatic restore also failed: %v", depErr, rbErr)
			}
			return rp, true, fmt.Errorf("dependent recreation failed: %v; original container restored", depErr)
		}
	}
	trigger := fmt.Sprintf("chain-recreate:%d", runID)
	if err := a.verifyChainLifecycleAction(ctx, hostID, container, trigger, actor, chainJobID, wasRunning); err != nil {
		releaseLease()
		rbErr := a.rollbackChainRestorePoint(context.Background(), runID, chainJobID, container, rp, actor, "failed-current-recreate")
		if rbErr != nil {
			return rp, true, fmt.Errorf("post-recreate verification failed: %v; automatic restore also failed: %v", err, rbErr)
		}
		return rp, true, fmt.Errorf("post-recreate verification failed: %v; original container restored", err)
	}
	if err := a.captureCurrentConfigDriftBaseline(ctx, hostID, container, "post-chain-recreate"); err != nil {
		_ = a.Store.AddJobLog(ctx, chainJobID, "WARN", "config-drift", "Could not refresh drift baseline after recreate: "+err.Error())
	}
	_ = a.Store.Audit(ctx, actor, "chain.step.recreate.success", hostID, container, fmt.Sprintf("run=%d restore_point=%d", runID, rp.ID))
	return rp, true, nil
}

func (a *App) executeUpdateChain(chain db.UpdateChain, steps []db.UpdateChainStep, chainJobID, runID int64, trigger, actor string) {
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Hour)
	defer cancel()
	defer a.releaseChainMembers(chain.ID, chain.HostID, steps)
	if !a.beginAsyncJob(ctx, chainJobID) {
		if job, err := a.Store.Job(context.Background(), chainJobID); err == nil && job.Status == "cancelled" {
			_ = a.Store.FinishUpdateChainRun(context.Background(), runID, "cancelled", "cancelled before execution")
			_ = a.Store.TouchUpdateChain(context.Background(), chain.ID, "cancelled")
			_ = a.Store.Audit(context.Background(), actor, "chain.cancelled", chain.HostID, "", fmt.Sprintf("chain=%d run=%d", chain.ID, runID))
		}
		return
	}
	a.jobProgress(ctx, chainJobID, 3, "Starting update chain")
	_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", fmt.Sprintf("chain started · %d step(s)", len(steps)))
	_ = a.Store.Audit(ctx, actor, "chain.started", chain.HostID, "", fmt.Sprintf("chain=%d run=%d", chain.ID, runID))

	// Phase 1 deliberately checks every member before performing any lifecycle
	// action. This is what guarantees that a completely current chain remains a
	// true no-op: no restart, no recreate and no completion notification.
	states := make(map[string]chainStepAvailability, len(steps))
	stepIDs := make(map[string]int64, len(steps))
	var chainErr error
	actionableUpdates := 0
	for i, st := range steps {
		pct := 5 + int(float64(i)/float64(maxInt(1, len(steps)))*15)
		a.jobProgress(ctx, chainJobID, pct, fmt.Sprintf("Checking step %d/%d · %s", i+1, len(steps), st.ContainerName))
		stepID, _ := a.Store.AddUpdateChainRunStep(ctx, db.UpdateChainRunStep{RunID: runID, Position: st.Position, ContainerName: st.ContainerName, Status: "checking", StartedAt: time.Now().UTC().Format(time.RFC3339)})
		stepIDs[st.ContainerName] = stepID
		_ = a.Store.Audit(ctx, actor, "chain.step.started", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d position=%d", chain.ID, runID, st.Position))
		checkCtx, checkCancel := context.WithTimeout(ctx, 20*time.Minute)
		_, _, checkErr := a.check(checkCtx, chain.HostID, st.ContainerName, fmt.Sprintf("chain-check:%d", runID))
		checkCancel()
		if checkErr != nil {
			chainErr = fmt.Errorf("%s update check failed: %w", st.ContainerName, checkErr)
			_ = a.Store.UpdateChainRunStep(ctx, stepID, "failed", 0, chainErr.Error(), true)
			break
		}
		cache, _ := a.Store.Cache(ctx, chain.HostID, st.ContainerName)
		snoozed := cacheHasSnoozedUpdate(cache)
		available := bool(cache.UpdateAvailable) && !snoozed
		states[st.ContainerName] = chainStepAvailability{UpdateAvailable: available, Snoozed: snoozed}
		if available {
			actionableUpdates++
		}
	}

	if chainErr == nil && actionableUpdates > 0 && trigger == "automatic" {
		a.jobProgress(ctx, chainJobID, 20, "Validating chain Preflight plan")
		blockedContainer := ""
		blockedReason := ""
		for _, st := range steps {
			state := states[st.ContainerName]
			if !state.UpdateAvailable {
				continue
			}
			preflight, _ := a.runUpdatePreflight(ctx, updateRequest{HostID: chain.HostID, Container: st.ContainerName, Trigger: fmt.Sprintf("chain-auto:%d:plan", runID), Actor: actor, AllowPreflightWarnings: bool(chain.AllowPreflightWarnings)}, false)
			if preflight.Status != "blocked" {
				continue
			}
			blockedContainer = st.ContainerName
			blockedReason = preflight.Summary
			for _, check := range preflight.Checks {
				if check.Status == preflightRed {
					blockedReason = check.Title + ": " + firstNonEmpty(check.Detail, check.Description)
					break
				}
			}
			break
		}
		if blockedContainer != "" {
			for _, st := range steps {
				status := "skipped_preflight"
				errText := ""
				if st.ContainerName == blockedContainer {
					status = "blocked_preflight"
					errText = blockedReason
				}
				_ = a.Store.UpdateChainRunStep(ctx, stepIDs[st.ContainerName], status, 0, errText, true)
			}
			msg := fmt.Sprintf("automatic chain held by Preflight at %s: %s", blockedContainer, blockedReason)
			_ = a.Store.AddJobLog(ctx, chainJobID, "WARN", "chain", msg)
			_ = a.Store.FinishUpdateChainRun(ctx, runID, "blocked", msg)
			_ = a.Store.TouchUpdateChain(ctx, chain.ID, "blocked")
			a.jobProgress(ctx, chainJobID, 100, "Automatic chain held by Preflight")
			_ = a.Store.FinishJob(ctx, chainJobID, "skipped", fmt.Sprintf(`{"updates":%d,"status":"blocked_preflight"}`, actionableUpdates), "")
			_ = a.Store.Audit(ctx, actor, "chain.blocked-preflight", chain.HostID, blockedContainer, fmt.Sprintf("chain=%d run=%d %s", chain.ID, runID, blockedReason))
			return
		}
	}

	if chainErr == nil && actionableUpdates == 0 {
		for _, st := range steps {
			stepID := stepIDs[st.ContainerName]
			state := states[st.ContainerName]
			status := "skipped_current"
			msg := st.ContainerName + " is already current; no lifecycle action required"
			if state.Snoozed {
				status = "skipped_snoozed"
				msg = st.ContainerName + " has a snoozed digest; no lifecycle action required"
			}
			_ = a.Store.UpdateChainRunStep(ctx, stepID, status, 0, "", true)
			_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", msg)
			_ = a.Store.Audit(ctx, actor, "chain.step.skipped", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d reason=%s", chain.ID, runID, status))
		}
		_ = a.Store.FinishUpdateChainRun(ctx, runID, "no_changes", "")
		_ = a.Store.TouchUpdateChain(ctx, chain.ID, "no_changes")
		a.jobProgress(ctx, chainJobID, 100, "No chain updates available")
		_ = a.Store.FinishJob(ctx, chainJobID, "success", `{"updates":0,"lifecycle_actions":0}`, "")
		_ = a.Store.Audit(ctx, actor, "chain.no_changes", chain.HostID, "", fmt.Sprintf("chain=%d run=%d", chain.ID, runID))
		// Intentionally no Pushover notification: a policy-run that only confirms
		// an already-current chain should be silent.
		return
	}

	completed := []completedChainAction{}
	// Data protection is transaction-scoped inside a chain: each effective
	// service/stack protection scope is cold-captured at most once per run.
	// Later steps still get their own writable-layer/config restore points, but
	// they reuse the already protected persistent-data baseline.
	capturedDataScopes := map[string]int64{}
	forceDataTransactionRollback := false
	blockedDuringExecution := false
	for i, st := range steps {
		if chainErr != nil {
			break
		}
		stepID := stepIDs[st.ContainerName]
		state := states[st.ContainerName]
		pct := 22 + int(float64(i)/float64(maxInt(1, len(steps)))*68)
		a.jobProgress(ctx, chainJobID, pct, fmt.Sprintf("Step %d/%d · %s", i+1, len(steps), st.ContainerName))

		if state.Snoozed {
			_ = a.Store.UpdateChainRunStep(ctx, stepID, "skipped_snoozed", 0, "", true)
			_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", st.ContainerName+" has a snoozed update; step skipped")
			_ = a.Store.Audit(ctx, actor, "chain.step.skipped", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d reason=snoozed", chain.ID, runID))
			chainErr = waitChainStep(ctx, st.WaitSeconds)
			continue
		}

		if !state.UpdateAvailable {
			action := normalizeChainCurrentAction(st.CurrentAction)
			switch action {
			case "restart":
				_ = a.Store.UpdateChainRunStep(ctx, stepID, "restarting", 0, "", false)
				performed, err := a.restartCurrentChainMember(ctx, runID, chainJobID, chain.HostID, st.ContainerName, actor)
				if err != nil {
					chainErr = fmt.Errorf("%s restart-if-current failed: %w", st.ContainerName, err)
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "failed", 0, chainErr.Error(), true)
				} else if performed {
					completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "restart"})
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "restarted", 0, "", true)
					_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", st.ContainerName+" was already current and restarted because this chain run contains updates")
				} else {
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "skipped_current", 0, "", true)
				}
			case "recreate":
				_ = a.Store.UpdateChainRunStep(ctx, stepID, "recreating", 0, "", false)
				dataScope := a.dataProtectionCaptureScope(ctx, chain.HostID, st.ContainerName)
				reusingDataBaseline := dataScope != "" && capturedDataScopes[dataScope] > 0
				rp, performed, err := a.recreateCurrentChainMember(ctx, runID, chainJobID, chain.HostID, st.ContainerName, actor, reusingDataBaseline)
				if err != nil {
					if reusingDataBaseline && performed {
						forceDataTransactionRollback = true
					}
					chainErr = fmt.Errorf("%s recreate-if-current failed: %w", st.ContainerName, err)
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "failed", 0, chainErr.Error(), true)
				} else if performed {
					if dataScope != "" && bool(rp.VolumeDataProtected) {
						capturedDataScopes[dataScope] = rp.ID
					}
					completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "recreate", RestorePointID: rp.ID})
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "recreated", 0, "", true)
					_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", fmt.Sprintf("%s was already current and safely recreated from the same image (restore point #%d)", st.ContainerName, rp.ID))
				} else {
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "skipped_current", 0, "", true)
				}
			default:
				_ = a.Store.UpdateChainRunStep(ctx, stepID, "skipped_current", 0, "", true)
				_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", st.ContainerName+" is already current; configured action is Skip")
				_ = a.Store.Audit(ctx, actor, "chain.step.skipped", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d reason=current", chain.ID, runID))
			}
			if chainErr == nil {
				chainErr = waitChainStep(ctx, st.WaitSeconds)
			}
		} else {
			stepTrigger := fmt.Sprintf("chain:%d", runID)
			if trigger == "automatic" {
				stepTrigger = fmt.Sprintf("chain-auto:%d", runID)
			}
			dataScope := a.dataProtectionCaptureScope(ctx, chain.HostID, st.ContainerName)
			reusingDataBaseline := dataScope != "" && capturedDataScopes[dataScope] > 0
			jobID, err := a.enqueueUpdate(ctx, chain.HostID, st.ContainerName, stepTrigger, actor, bool(chain.AllowPreflightWarnings), reusingDataBaseline)
			if err != nil {
				chainErr = fmt.Errorf("%s could not be queued: %w", st.ContainerName, err)
				_ = a.Store.UpdateChainRunStep(ctx, stepID, "failed", 0, chainErr.Error(), true)
			} else {
				_ = a.Store.UpdateChainRunStep(ctx, stepID, "updating", jobID, "", false)
				job, waitErr := a.waitForJob(ctx, jobID)
				transactionWasDestructive := false
				if tx, txErr := a.Store.UpdateTransactionByJob(ctx, jobID); txErr == nil {
					if tx.RestorePointID > 0 {
						if rp, rpErr := a.Store.RestorePoint(ctx, tx.RestorePointID); rpErr == nil && dataScope != "" && bool(rp.VolumeDataProtected) {
							capturedDataScopes[dataScope] = rp.ID
						}
					}
					if events, eventsErr := a.Store.UpdateTransactionEvents(ctx, tx.ID); eventsErr == nil {
						for _, event := range events {
							if event.ToState == txUpdating {
								transactionWasDestructive = true
								break
							}
						}
					}
				}
				if waitErr != nil {
					chainErr = fmt.Errorf("%s update wait failed: %w", st.ContainerName, waitErr)
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "failed", jobID, chainErr.Error(), true)
				} else if job.Status == "skipped" {
					blockedDuringExecution = true
					chainErr = fmt.Errorf("%s automatic update held by Preflight", st.ContainerName)
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "blocked_preflight", jobID, chainErr.Error(), true)
				} else if job.Status != "success" {
					if reusingDataBaseline && transactionWasDestructive {
						forceDataTransactionRollback = true
					}
					chainErr = fmt.Errorf("%s update failed: %s", st.ContainerName, job.Error)
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "failed", jobID, job.Error, true)
				} else {
					completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "update"})
					_ = a.Store.UpdateChainRunStep(ctx, stepID, "success", jobID, "", true)
					_ = a.Store.Audit(ctx, actor, "chain.step.success", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d job=%d", chain.ID, runID, jobID))
					if st.WaitSeconds > 0 {
						_ = a.Store.AddJobLog(ctx, chainJobID, "INFO", "chain", fmt.Sprintf("waiting %ds after %s", st.WaitSeconds, st.ContainerName))
						chainErr = waitChainStep(ctx, st.WaitSeconds)
					}
				}
			}
		}

		if chainErr != nil {
			if blockedDuringExecution {
				_ = a.Store.Audit(context.Background(), actor, "chain.step.blocked-preflight", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d error=%s", chain.ID, runID, chainErr.Error()))
				break
			}
			_ = a.Store.Audit(context.Background(), actor, "chain.step.failed", chain.HostID, st.ContainerName, fmt.Sprintf("chain=%d run=%d error=%s", chain.ID, runID, chainErr.Error()))
			critical := criticalChainFailure(chainErr)
			if bool(chain.StopOnFailure) || critical {
				if critical && !bool(chain.StopOnFailure) {
					_ = a.Store.AddJobLog(ctx, chainJobID, "ERROR", "chain", "critical safety failure stops the chain even though Stop on Failure is disabled: "+chainErr.Error())
				}
				break
			}
			_ = a.Store.AddJobLog(ctx, chainJobID, "WARN", "chain", "non-critical step failure; chain is configured to continue: "+chainErr.Error())
			chainErr = nil
		}
	}

	if chainErr != nil && blockedDuringExecution {
		msg := chainErr.Error()
		_ = a.Store.AddJobLog(ctx, chainJobID, "WARN", "chain", msg)
		_ = a.Store.FinishUpdateChainRun(ctx, runID, "blocked", msg)
		_ = a.Store.TouchUpdateChain(ctx, chain.ID, "blocked")
		a.jobProgress(ctx, chainJobID, 100, "Automatic chain held by Preflight")
		_ = a.Store.FinishJob(ctx, chainJobID, "skipped", fmt.Sprintf(`{"updates":%d,"completed_actions":%d,"status":"blocked_preflight"}`, actionableUpdates, len(completed)), "")
		_ = a.Store.Audit(ctx, actor, "chain.blocked-preflight", chain.HostID, "", fmt.Sprintf("chain=%d run=%d error=%s", chain.ID, runID, msg))
		return
	}

	if chainErr != nil && (bool(chain.RollbackCompleted) || forceDataTransactionRollback) && len(completed) > 0 {
		if forceDataTransactionRollback && !bool(chain.RollbackCompleted) {
			_ = a.Store.AddJobLog(ctx, chainJobID, "WARN", "chain", "shared protected data may have been touched by a failed step; rolling back completed members to keep software and data on the same chain baseline")
		}
		a.jobProgress(ctx, chainJobID, 94, "Rolling back completed chain members")
		failedRollbacks := a.rollbackCompletedChainMembers(ctx, runID, chainJobID, completed, chain.HostID, actor)
		if len(failedRollbacks) > 0 {
			chainErr = fmt.Errorf("%v; rollback of completed members incomplete: %s", chainErr, strings.Join(failedRollbacks, "; "))
		}
	}
	if chainErr != nil {
		_ = a.Store.FinishUpdateChainRun(ctx, runID, "failed", chainErr.Error())
		_ = a.Store.TouchUpdateChain(ctx, chain.ID, "failed")
		a.jobProgress(ctx, chainJobID, 100, "Update chain failed")
		_ = a.Store.FinishJob(ctx, chainJobID, "failed", "", chainErr.Error())
		_ = a.Store.Audit(ctx, actor, "chain.failed", chain.HostID, "", fmt.Sprintf("chain=%d run=%d error=%s", chain.ID, runID, chainErr.Error()))
		event := "manual_update"
		if trigger == "automatic" {
			event = "auto"
		}
		go a.notifyHostUsers(chain.HostID, event, "", "Update chain failed · "+chain.Name, chainErr.Error(), "")
		return
	}
	_ = a.Store.FinishUpdateChainRun(ctx, runID, "success", "")
	_ = a.Store.TouchUpdateChain(ctx, chain.ID, "success")
	a.jobProgress(ctx, chainJobID, 100, "Update chain completed")
	_ = a.Store.FinishJob(ctx, chainJobID, "success", fmt.Sprintf(`{"updates":%d,"completed_actions":%d}`, actionableUpdates, len(completed)), "")
	_ = a.Store.Audit(ctx, actor, "chain.success", chain.HostID, "", fmt.Sprintf("chain=%d run=%d updates=%d actions=%d", chain.ID, runID, actionableUpdates, len(completed)))
	event := "manual_update"
	if trigger == "automatic" {
		event = "auto"
	}
	go a.notifyHostUsers(chain.HostID, event, "", "Update chain completed · "+chain.Name, fmt.Sprintf("%d update(s) processed successfully; %d lifecycle action(s) completed.", actionableUpdates, len(completed)), "")
}

func (a *App) startAutomationChains(ctx context.Context, automation db.Automation, hostIDs []int64) {
	chains, err := a.Store.UpdateChains(ctx)
	if err != nil {
		return
	}
	hostSet := map[int64]bool{}
	for _, id := range hostIDs {
		hostSet[id] = true
	}
	for _, chain := range chains {
		if chain.AutomationID != automation.ID || !hostSet[chain.HostID] {
			continue
		}
		if normalizeChainPolicyMode(chain.PolicyMode) != "auto" {
			continue
		}
		if _, _, err := a.startUpdateChain(ctx, chain.ID, "automatic", "scheduler"); err != nil {
			_ = a.Store.TouchUpdateChain(ctx, chain.ID, "blocked")
			_ = a.Store.Audit(ctx, "scheduler", "chain.blocked", chain.HostID, "", fmt.Sprintf("chain=%d automation=%d error=%s", chain.ID, automation.ID, err.Error()))
		}
	}
}
