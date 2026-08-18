package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const LevelTrace = slog.Level(-8)

type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

func NewRotatingWriter(path string, maxMB, backups int) (*RotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &RotatingWriter{path: path, maxBytes: int64(maxMB) * 1024 * 1024, backups: backups}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file, w.size = f, st.Size()
	return nil
}

func (w *RotatingWriter) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
	}
	for i := w.backups - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", w.path, i)
		next := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(old); err == nil {
			_ = os.Rename(old, next)
		}
	}
	if w.backups > 0 {
		_ = os.Rename(w.path, w.path+".1")
	} else {
		_ = os.Remove(w.path)
	}
	return w.open()
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.maxBytes > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
		w.size = 0
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func ParseLevel(v string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "TRACE":
		return LevelTrace
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func New(path, level string) (*slog.Logger, *slog.LevelVar, error) {
	rw, err := NewRotatingWriter(path, 25, 5)
	if err != nil {
		return nil, nil, err
	}
	lv := &slog.LevelVar{}
	lv.Set(ParseLevel(level))
	out := io.MultiWriter(os.Stdout, rw)
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lv})
	return slog.New(h), lv, nil
}
