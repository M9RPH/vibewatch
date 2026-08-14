package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRestorePointDatabaseJSONHydrationKeepsRawMetadataPrivate(t *testing.T) {
	raw := `[{"id":1,"host_id":2,"container_name":"svc","dependencies_json":"[{\"type\":\"network_namespace\"}]","data_manifest_json":"{\"scope_type\":\"stack\"}","dependency_count":1,"data_bytes":42}]`
	var rows []RestorePoint
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one restore point, got %d", len(rows))
	}
	rp := rows[0]
	if rp.DependenciesJSON == "" || !strings.Contains(rp.DependenciesJSON, "network_namespace") {
		t.Fatalf("dependencies_json was not hydrated: %#v", rp)
	}
	if rp.DataManifestJSON == "" || !strings.Contains(rp.DataManifestJSON, "scope_type") {
		t.Fatalf("data_manifest_json was not hydrated: %#v", rp)
	}
	encoded, err := json.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	out := string(encoded)
	if strings.Contains(out, "dependencies_json") || strings.Contains(out, "data_manifest_json") || strings.Contains(out, "network_namespace") {
		t.Fatalf("raw restore metadata leaked through public JSON: %s", out)
	}
}
