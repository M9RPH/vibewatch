package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmptyAssignmentArraysRemainPresentInJSON(t *testing.T) {
	group := HostGroup{ID: 1, Name: "empty", HostIDs: []int64{}}
	gb, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gb), `"host_ids":[]`) {
		t.Fatalf("empty host group must serialize host_ids as []: %s", gb)
	}

	user := User{ID: 2, Username: "demo", Role: "user", HostIDs: []int64{}, GroupIDs: []int64{}}
	ub, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ub), `"host_ids":[]`) || !strings.Contains(string(ub), `"group_ids":[]`) {
		t.Fatalf("empty user assignments must remain explicit arrays: %s", ub)
	}
}
