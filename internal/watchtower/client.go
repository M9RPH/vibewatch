package watchtower

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct{ HTTP *http.Client }

func New() *Client {
	return &Client{HTTP: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 31 * time.Minute}}
}

type CheckItem struct {
	Name            string `json:"name"`
	Image           string `json:"image"`
	ImageID         string `json:"image_id"`
	Digest          string `json:"digest"`
	UpdateAvailable bool   `json:"update_available"`
	LatestImageID   string `json:"latest_image_id"`
	LatestDigest    string `json:"latest_digest"`
	Error           string `json:"error"`
}
type CheckResponse struct {
	Containers []CheckItem `json:"containers"`
	Count      int         `json:"count"`
}
type UpdateSummary struct {
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}
type UpdateResponse struct {
	Summary UpdateSummary `json:"summary"`
	Error   string        `json:"error,omitempty"`
}

func (c *Client) request(ctx context.Context, method, rawURL, token string, dst any) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(nil))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("watchtower HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if dst != nil && len(body) > 0 {
		if err := json.Unmarshal(body, dst); err != nil {
			return body, err
		}
	}
	return body, nil
}

func (c *Client) WaitReadyFor(ctx context.Context, base string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		if err := waitCtx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("vibewatch worker did not become ready within %s: %v", timeout, lastErr)
			}
			return fmt.Errorf("vibewatch worker did not become ready within %s: %w", timeout, err)
		}
		probeCtx, probeCancel := context.WithTimeout(waitCtx, 5*time.Second)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/readyz", nil)
		resp, err := c.HTTP.Do(req)
		probeCancel()
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("readyz returned HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}
func (c *Client) Check(ctx context.Context, base, token, container string) (CheckResponse, []byte, error) {
	u := base + "/v1/check"
	if container != "" {
		u += "?container=" + url.QueryEscape(container)
	}
	var r CheckResponse
	b, err := c.request(ctx, http.MethodPost, u, token, &r)
	return r, b, err
}
func (c *Client) Update(ctx context.Context, base, token, container string) (UpdateResponse, []byte, error) {
	u := base + "/v1/update?timeout=30m"
	if container != "" {
		u += "&container=" + url.QueryEscape(container)
	}
	var r UpdateResponse
	b, err := c.request(ctx, http.MethodPost, u, token, &r)
	return r, b, err
}
