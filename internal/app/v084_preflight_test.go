package app

import "testing"

func TestPreflightResultClassification(t *testing.T) {
	cases := []struct {
		name   string
		checks []PreflightCheck
		want   string
	}{
		{name: "ready", checks: []PreflightCheck{{Status: preflightGreen}}, want: "ready"},
		{name: "warning", checks: []PreflightCheck{{Status: preflightGreen}, {Status: preflightYellow}}, want: "ready_with_warnings"},
		{name: "blocked", checks: []PreflightCheck{{Status: preflightYellow}, {Status: preflightRed}}, want: "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := PreflightResult{}
			for i, c := range tc.checks {
				r.add("check", c.Status, "check", "description", "")
				_ = i
			}
			r.finish()
			if r.Status != tc.want {
				t.Fatalf("status=%q want %q", r.Status, tc.want)
			}
			if tc.want == "blocked" && r.Blocked == 0 {
				t.Fatal("blocked result must retain blocking count")
			}
		})
	}
}
