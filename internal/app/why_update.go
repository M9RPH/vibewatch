package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

type updateWhyReason struct {
	Tone   string `json:"tone"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

func whyShortDigest(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 24 {
		return v[:24] + "…"
	}
	return v
}

type updateWhyResponse struct {
	Status     string            `json:"status"`
	Summary    string            `json:"summary"`
	NextAction string            `json:"next_action,omitempty"`
	Reasons    []updateWhyReason `json:"reasons"`
}

func (a *App) automationTargetsHost(ctx context.Context, automation db.Automation, hostID int64) bool {
	if normalizeAutomationKind(automation.Kind) != "policy" || !bool(automation.Enabled) {
		return false
	}
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

func (a *App) enabledAutomationForHost(ctx context.Context, hostID int64) (db.Automation, bool) {
	xs, err := a.Store.Automations(ctx)
	if err != nil {
		return db.Automation{}, false
	}
	for _, x := range xs {
		if a.automationTargetsHost(ctx, x, hostID) {
			return x, true
		}
	}
	return db.Automation{}, false
}

func (a *App) handleWhyUpdate(w http.ResponseWriter, r *http.Request, hostID int64) {
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	if container == "" {
		writeErr(w, 400, "container is required")
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	res := updateWhyResponse{Status: "ready", Summary: "No blocking condition is currently known.", Reasons: []updateWhyReason{}}
	add := func(tone, title, detail string) {
		res.Reasons = append(res.Reasons, updateWhyReason{Tone: tone, Title: title, Detail: detail})
	}

	if managed, role := a.systemManagedContainer(container); managed {
		res.Status = "blocked"
		res.Summary = "This is a Vibewatch-managed system container."
		res.NextAction = "Use Owner Settings for Vibewatch/worker maintenance."
		add("red", "Managed by Vibewatch", fmt.Sprintf("%s is a protected %s container and is not updated from the normal Containers workflow.", container, role))
		writeJSON(w, 200, res)
		return
	}
	if !bool(h.Enabled) {
		res.Status = "blocked"
		res.Summary = "The Docker host is disabled."
		res.NextAction = "Enable the host first."
		add("red", "Host disabled", "Disabled hosts are not processed by policy runs.")
	}
	health := a.hostHealthView(hostID)
	if health.Known && !health.Reachable {
		res.Status = "blocked"
		res.Summary = "The Docker host is currently unreachable."
		res.NextAction = "Restore Docker connectivity and retry the check."
		add("red", "Docker unreachable", firstNonEmpty(health.Error, "The last Docker reachability probe failed."))
	} else if health.Checking {
		add("blue", "Docker reachability is being refreshed", "Cached state may be shown while the host probe is running.")
	} else if health.Reachable {
		add("green", "Docker host reachable", firstNonEmpty(health.Version, "The last reachability probe succeeded."))
	}

	jobs, _ := a.Store.Jobs(r.Context(), 5000)
	for _, j := range jobs {
		if j.HostID == hostID && j.ContainerName == container && (j.Status == "queued" || j.Status == "running") {
			res.Status = "waiting"
			res.Summary = "An operation for this container is already in progress."
			res.NextAction = "Wait for the existing job to finish or cancel it while it is still queued."
			add("blue", fmt.Sprintf("Job #%d · %s", j.ID, j.Status), j.Type+" · "+j.Trigger)
			break
		}
	}
	leases, _ := a.Store.OperationLeases(r.Context())
	for _, l := range leases {
		if l.HostID == hostID && (l.ContainerName == container || l.ContainerName == "") {
			add("blue", "Operation lease active", fmt.Sprintf("%s is holding %s until %s", l.Owner, l.OperationType, l.ExpiresAt))
			if res.Status == "ready" {
				res.Status = "waiting"
				res.Summary = "A protected Docker operation currently owns this resource."
				res.NextAction = "Wait for the active operation to finish."
			}
			break
		}
	}

	p, _ := a.Store.Policy(r.Context(), hostID, container)
	mode := p.Mode
	if mode == "" {
		mode = "manual"
	}
	if cm, ok := a.stackChainForMember(r.Context(), hostID, container); ok {
		mode = cm.PolicyMode
		add("blue", "Managed by update chain", fmt.Sprintf("%s · stack %s · policy %s", cm.ChainName, cm.ScopeKey, cm.PolicyMode))
		switch mode {
		case "ignore":
			res.Status = "blocked"
			res.Summary = "The stack is excluded by its Update Chain."
			res.NextAction = "Change the Stack Chain policy if updates should be allowed."
			add("red", "Chain policy · Excluded", "The container policy is intentionally controlled by the Stack Chain.")
		case "manual":
			if res.Status == "ready" {
				res.Status = "managed"
				res.Summary = "The Stack Chain is configured for manual updates."
				res.NextAction = "Run the Update Chain manually when you are ready."
			}
			add("yellow", "Chain policy · Manual", "Automatic installation is disabled for this stack.")
		case "auto":
			if cm.AllowPreflightWarnings {
				add("yellow", "Preflight safety · advisory warnings allowed", "Critical warnings and blockers still require manual approval.")
			} else {
				add("green", "Preflight safety · clean required", "Any Preflight warning holds the automatic chain before update execution.")
			}
			if cm.AutomationID <= 0 {
				res.Status = "blocked"
				res.Summary = "The Stack Chain is Auto Update but has no Policy Run assigned."
				res.NextAction = "Assign an Automation/Policy Run to the chain."
				add("red", "No Policy Run assigned", "Automatic chains only execute through the existing Automation scheduler.")
			} else if au, e := a.Store.Automation(r.Context(), cm.AutomationID); e != nil || !bool(au.Enabled) || !a.automationTargetsHost(r.Context(), au, hostID) {
				res.Status = "blocked"
				res.Summary = "The Stack Chain's Policy Run is unavailable or paused."
				res.NextAction = "Enable or correct the Automation assigned to this chain."
				add("red", "Policy Run unavailable", fmt.Sprintf("Automation #%d is missing, disabled or does not target this host.", cm.AutomationID))
			} else {
				add("green", "Automatic chain scheduled", fmt.Sprintf("%s · %s", au.Name, au.Cron))
			}
		}
	} else {
		switch mode {
		case "ignore":
			res.Status = "blocked"
			res.Summary = "This container is excluded from updates."
			res.NextAction = "Change its policy from Excluded if it should be checked/updated."
			add("red", "Policy · Excluded", "Excluded containers use read-only registry checks and are never installed automatically.")
		case "manual":
			if res.Status == "ready" {
				res.Status = "managed"
				res.Summary = "An update will only be installed manually."
				res.NextAction = "Use Update when a new digest is available."
			}
			add("yellow", "Policy · Manual", "Automatic installation is disabled.")
		case "auto":
			if bool(p.AllowPreflightWarnings) {
				add("yellow", "Preflight safety · advisory warnings allowed", "Critical warnings and blockers still require manual approval.")
			} else {
				add("green", "Preflight safety · clean required", "Any Preflight warning holds the automatic update before execution.")
			}
			if au, ok := a.enabledAutomationForHost(r.Context(), hostID); !ok {
				res.Status = "blocked"
				res.Summary = "Auto Update is enabled, but no active Policy Run targets this host."
				res.NextAction = "Create or enable an Automation for this host/group/all hosts."
				add("red", "No active Policy Run", "Auto Update policies are executed by Automation; there is no independent scheduler.")
			} else {
				add("green", "Auto Update scheduled", fmt.Sprintf("%s · %s", au.Name, au.Cron))
			}
		default:
			add("yellow", "Policy state unknown", "The container falls back to manual behavior.")
		}
	}

	cache, _ := a.Store.Cache(r.Context(), hostID, container)
	if strings.TrimSpace(cache.LastError) != "" {
		add("red", "Last update check failed", cache.LastError)
		if res.Status == "ready" || res.Status == "managed" {
			res.Status = "blocked"
			res.Summary = "Vibewatch could not complete the latest image check."
			res.NextAction = "Run Check again after resolving the registry/worker error."
		}
	}
	if strings.TrimSpace(cache.LastCheckedAt) == "" {
		add("yellow", "No completed image check yet", "Vibewatch has not recorded an authoritative digest comparison for this container.")
		if res.NextAction == "" {
			res.NextAction = "Run Check to populate the current image state."
		}
	}
	if bool(cache.UpdateAvailable) {
		if cache.SnoozedDigest != "" && cache.LatestDigest != "" && cache.SnoozedDigest == cache.LatestDigest {
			res.Status = "snoozed"
			res.Summary = "The currently available digest is snoozed."
			res.NextAction = "Unsnooze it, or wait until a different digest is published."
			add("yellow", "Current digest snoozed", whyShortDigest(cache.SnoozedDigest))
		} else {
			add("green", "New image digest available", whyShortDigest(cache.LatestDigest))
			if res.Status == "current" {
				res.Status = "ready"
			}
		}
	} else if cache.LastCheckedAt != "" && cache.LastError == "" {
		if res.Status == "ready" || res.Status == "managed" {
			res.Status = "current"
			res.Summary = "The last completed check found no newer image digest."
			if mode == "auto" {
				res.NextAction = "No action is required; the next Policy Run will check again."
			} else {
				res.NextAction = "No update is currently available."
			}
		}
		add("green", "Image is current", fmt.Sprintf("Last checked %s", cache.LastCheckedAt))
	}

	if cm, ok := a.stackChainForMember(r.Context(), hostID, container); ok && cm.PolicyMode == "auto" && cm.LastStatus == "blocked" && bool(cache.UpdateAvailable) && !cacheHasSnoozedUpdate(cache) {
		add("yellow", "Last automatic chain was held by Preflight", fmt.Sprintf("%s · review Chain history for the blocked step.", cm.ChainName))
		res.Status = "held"
		res.Summary = "The latest automatic chain run was safely held by Preflight."
		res.NextAction = "Open Update Chains, review the held run, then use Run Now for a one-time manual approval."
	}

	history, _ := a.Store.UpdateHistory(r.Context(), 1, hostID, container)
	if len(history) > 0 && history[0].PreflightStatus == "blocked" {
		if history[0].Status == "skipped" {
			add("yellow", "Last Auto Update was held by Preflight", firstNonEmpty(history[0].Error, history[0].PreflightDetails))
			if bool(cache.UpdateAvailable) && res.Status != "snoozed" {
				res.Status = "held"
				res.Summary = "The latest automatic update was safely held by Preflight."
				res.NextAction = "Review the warning, then run Update manually for a one-time approval; only allow advisory warnings if this risk should be accepted automatically."
			}
		} else {
			add("red", "Last update attempt was blocked by Preflight", firstNonEmpty(history[0].Error, history[0].PreflightDetails))
			if bool(cache.UpdateAvailable) && res.Status != "snoozed" {
				res.Status = "blocked"
				res.Summary = "The latest update attempt was blocked by Preflight."
				res.NextAction = "Open the update Preflight details and resolve the red check before retrying."
			}
		}
	}
	writeJSON(w, 200, res)
}
