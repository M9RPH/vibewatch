package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func envInt(name string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "fixture-v1"
	}
	body := os.Getenv("HEALTH_BODY")
	if body == "" {
		body = "healthy"
	}
	delay := time.Duration(envInt("HEALTH_DELAY_SECONDS", 0)) * time.Second
	started := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(started) < delay {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, "warming")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, body)
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, version)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "vibewatch integration fixture")
	})

	log.Printf("fixture listening on %s, health delay=%s", addr, delay)
	log.Fatal(http.ListenAndServe(addr, mux))
}
