package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Match implements a practical 5-field cron subset: *, */n, n, a-b, comma lists.
func Match(expr string, t time.Time) bool {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return false
	}
	vals := []int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	mins := []int{0, 0, 1, 1, 0}
	maxs := []int{59, 23, 31, 12, 6}
	for i := 0; i < 5; i++ {
		if !fieldMatch(f[i], vals[i], mins[i], maxs[i]) {
			return false
		}
	}
	return true
}

func Validate(expr string) error {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return fmt.Errorf("cron must have 5 fields: minute hour day month weekday")
	}
	mins := []int{0, 0, 1, 1, 0}
	maxs := []int{59, 23, 31, 12, 6}
	for i, v := range f {
		if !fieldValid(v, mins[i], maxs[i]) {
			return fmt.Errorf("invalid cron field %d: %s", i+1, v)
		}
	}
	return nil
}

func fieldMatch(s string, v, min, max int) bool {
	if !fieldValid(s, min, max) {
		return false
	}
	for _, p := range strings.Split(s, ",") {
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, "*/") {
			n, _ := strconv.Atoi(strings.TrimPrefix(p, "*/"))
			return n > 0 && (v-min)%n == 0
		}
		if strings.Contains(p, "-") {
			ab := strings.SplitN(p, "-", 2)
			a, _ := strconv.Atoi(ab[0])
			b, _ := strconv.Atoi(ab[1])
			if v >= a && v <= b {
				return true
			}
			continue
		}
		n, _ := strconv.Atoi(p)
		if v == n {
			return true
		}
	}
	return false
}
func fieldValid(s string, min, max int) bool {
	if s == "" {
		return false
	}
	for _, p := range strings.Split(s, ",") {
		if p == "*" {
			continue
		}
		if strings.HasPrefix(p, "*/") {
			n, e := strconv.Atoi(strings.TrimPrefix(p, "*/"))
			if e != nil || n < 1 {
				return false
			}
			continue
		}
		if strings.Contains(p, "-") {
			ab := strings.SplitN(p, "-", 2)
			if len(ab) != 2 {
				return false
			}
			a, e1 := strconv.Atoi(ab[0])
			b, e2 := strconv.Atoi(ab[1])
			if e1 != nil || e2 != nil || a < min || b > max || a > b {
				return false
			}
			continue
		}
		n, e := strconv.Atoi(p)
		if e != nil || n < min || n > max {
			return false
		}
	}
	return true
}
