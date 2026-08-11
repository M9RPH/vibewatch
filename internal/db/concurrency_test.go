package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreSerializesSQLiteCLIAndSetsBusyTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "sqlite3")
	lockDir := filepath.Join(dir, "active")
	argsLog := filepath.Join(dir, "args.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SQLITE_ARGS"
if ! mkdir "$FAKE_SQLITE_LOCK" 2>/dev/null; then
  echo concurrent >&2
  exit 99
fi
sleep 0.02
rmdir "$FAKE_SQLITE_LOCK"
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SQLITE_LOCK", lockDir)
	t.Setenv("FAKE_SQLITE_ARGS", argsLog)

	store := New(filepath.Join(dir, "test.db"))
	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- store.exec(context.Background(), "SELECT 1;")
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent sqlite call escaped store serialization: %v", err)
		}
	}

	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), ".timeout 10000") {
		t.Fatalf("sqlite CLI did not receive busy timeout: %s", logged)
	}
}
