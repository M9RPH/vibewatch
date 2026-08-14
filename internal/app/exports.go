package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
)

func exportFilename(kind, ext string) string {
	return fmt.Sprintf("vibewatch-%s-%s.%s", kind, time.Now().UTC().Format("20060102-150405"), ext)
}

func writeJSONDownload(w http.ResponseWriter, filename string, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

func writeCSVDownload(w http.ResponseWriter, filename string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	for _, row := range rows {
		_ = cw.Write(row)
	}
	cw.Flush()
}

func filterJobsForIdentity(a *App, r *http.Request, xs []db.Job) []db.Job {
	if a.isAdmin(r) {
		return xs
	}
	allowed := a.allowedHostSet(r.Context(), a.identity(r))
	out := make([]db.Job, 0, len(xs))
	for _, x := range xs {
		if allowed[x.HostID] {
			out = append(out, x)
		}
	}
	return out
}
func filterHistoryForIdentity(a *App, r *http.Request, xs []db.UpdateHistory) []db.UpdateHistory {
	if a.isAdmin(r) {
		return xs
	}
	allowed := a.allowedHostSet(r.Context(), a.identity(r))
	out := make([]db.UpdateHistory, 0, len(xs))
	for _, x := range xs {
		if allowed[x.HostID] {
			out = append(out, x)
		}
	}
	return out
}
func filterAuditForIdentity(a *App, r *http.Request, xs []db.Audit) []db.Audit {
	if a.isAdmin(r) {
		return xs
	}
	allowed := a.allowedHostSet(r.Context(), a.identity(r))
	actor := a.actor(r)
	out := make([]db.Audit, 0, len(xs))
	for _, x := range xs {
		if x.Actor == actor || (x.HostID > 0 && allowed[x.HostID]) {
			out = append(out, x)
		}
	}
	return out
}
func filterDockerEventsForIdentity(a *App, r *http.Request, xs []db.DockerEvent) []db.DockerEvent {
	if a.isAdmin(r) {
		return xs
	}
	allowed := a.allowedHostSet(r.Context(), a.identity(r))
	out := make([]db.DockerEvent, 0, len(xs))
	for _, x := range xs {
		if allowed[x.HostID] {
			out = append(out, x)
		}
	}
	return out
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	kind := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/export/"), "/")
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" && format != "txt" {
		writeErr(w, 400, "format must be json, csv or txt")
		return
	}

	switch kind {
	case "history":
		xs, err := a.Store.UpdateHistory(r.Context(), 5000, 0, "")
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		xs = filterHistoryForIdentity(a, r, xs)
		if format == "json" {
			writeJSONDownload(w, exportFilename(kind, "json"), xs)
			return
		}
		if format != "csv" {
			writeErr(w, 400, "history supports json or csv")
			return
		}
		rows := make([][]string, 0, len(xs))
		for _, x := range xs {
			rows = append(rows, []string{strconv.FormatInt(x.ID, 10), x.TS, strconv.FormatInt(x.HostID, 10), x.ContainerName, x.Action, x.Trigger, x.Actor, x.Status, x.FromVersion, x.ToVersion, x.FromDigest, x.ToDigest, x.PreflightStatus, x.VerificationStatus, strconv.FormatInt(x.RestorePointID, 10), strconv.FormatInt(x.DurationMS, 10), x.Error})
		}
		writeCSVDownload(w, exportFilename(kind, "csv"), []string{"id", "timestamp", "host_id", "container", "action", "trigger", "actor", "status", "from_version", "to_version", "from_digest", "to_digest", "preflight_status", "verification_status", "restore_point_id", "duration_ms", "error"}, rows)
	case "jobs":
		xs, err := a.Store.Jobs(r.Context(), 5000)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		xs = filterJobsForIdentity(a, r, xs)
		if format == "json" {
			writeJSONDownload(w, exportFilename(kind, "json"), xs)
			return
		}
		if format != "csv" {
			writeErr(w, 400, "jobs supports json or csv")
			return
		}
		rows := make([][]string, 0, len(xs))
		for _, x := range xs {
			rows = append(rows, []string{strconv.FormatInt(x.ID, 10), x.Type, x.Trigger, strconv.FormatInt(x.HostID, 10), x.ContainerName, x.Status, x.StartedAt, x.FinishedAt, x.SummaryJSON, x.Error})
		}
		writeCSVDownload(w, exportFilename(kind, "csv"), []string{"id", "type", "trigger", "host_id", "container", "status", "started_at", "finished_at", "summary", "error"}, rows)
	case "audit":
		xs, err := a.Store.Audits(r.Context(), 5000)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		xs = filterAuditForIdentity(a, r, xs)
		if format == "json" {
			writeJSONDownload(w, exportFilename(kind, "json"), xs)
			return
		}
		if format != "csv" {
			writeErr(w, 400, "audit supports json or csv")
			return
		}
		rows := make([][]string, 0, len(xs))
		for _, x := range xs {
			rows = append(rows, []string{strconv.FormatInt(x.ID, 10), x.TS, x.Actor, x.Action, strconv.FormatInt(x.HostID, 10), x.ContainerName, x.Details})
		}
		writeCSVDownload(w, exportFilename(kind, "csv"), []string{"id", "timestamp", "actor", "action", "host_id", "container", "details"}, rows)
	case "docker-events":
		xs, err := a.Store.DockerEvents(r.Context(), 0, 5000)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		xs = filterDockerEventsForIdentity(a, r, xs)
		if format == "json" {
			writeJSONDownload(w, exportFilename(kind, "json"), xs)
			return
		}
		if format != "csv" {
			writeErr(w, 400, "docker-events supports json or csv")
			return
		}
		rows := make([][]string, 0, len(xs))
		for _, x := range xs {
			rows = append(rows, []string{strconv.FormatInt(x.ID, 10), x.TS, strconv.FormatInt(x.HostID, 10), x.RawJSON})
		}
		writeCSVDownload(w, exportFilename(kind, "csv"), []string{"id", "timestamp", "host_id", "raw_json"}, rows)
	case "pushover":
		id := a.identity(r)
		var userID *int64
		if id.Role != "owner" && id.Role != "admin" {
			uid := id.UserID
			userID = &uid
		}
		xs, err := a.Store.NotificationDeliveries(r.Context(), userID, 5000)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if format == "json" {
			writeJSONDownload(w, exportFilename(kind, "json"), xs)
			return
		}
		if format != "csv" {
			writeErr(w, 400, "pushover supports json or csv")
			return
		}
		rows := make([][]string, 0, len(xs))
		for _, x := range xs {
			rows = append(rows, []string{strconv.FormatInt(x.ID, 10), x.TS, strconv.FormatInt(x.UserID, 10), x.Username, strconv.FormatInt(x.HostID, 10), x.ContainerName, x.Event, x.Title, x.Status, x.Error})
		}
		writeCSVDownload(w, exportFilename(kind, "csv"), []string{"id", "timestamp", "user_id", "username", "host_id", "container", "event", "title", "status", "error"}, rows)
	case "application":
		if !a.requireAdmin(w, r) {
			return
		}
		if format != "txt" {
			writeErr(w, 400, "application supports txt")
			return
		}
		path := filepath.Join(a.Cfg.DataDir, "logs", "app.log")
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, exportFilename(kind, "txt")))
		_, _ = w.Write(b)
	default:
		writeErr(w, 404, "unknown export")
	}
}
