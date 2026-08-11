package app

import "testing"

func TestSystemManagedContainerDetection(t *testing.T) {
	a := &App{Cfg: Config{ControllerName: "vibewatch"}}
	cases := []struct {
		name    string
		managed bool
		role    string
	}{
		{"watchtower-ui", true, "controller"},
		{"vibewatch", true, "controller"},
		{"watchtower-ui-worker-2", true, "worker"},
		{"vibewatch-worker-7", true, "worker"},
		{"watchtower-ui-self-updater", true, "maintenance"},
		{"nickfedor-watchtower-user", false, ""},
		{"netbird", false, ""},
	}
	for _, tc := range cases {
		got, role := a.systemManagedContainer(tc.name)
		if got != tc.managed || role != tc.role {
			t.Fatalf("%s: managed=%v role=%q, want %v %q", tc.name, got, role, tc.managed, tc.role)
		}
	}
}
