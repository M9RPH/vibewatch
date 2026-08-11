package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Pushover struct {
	HTTP     *http.Client
	Endpoint string
}

func NewPushover() *Pushover {
	return &Pushover{HTTP: &http.Client{Timeout: 10 * time.Second}, Endpoint: "https://api.pushover.net/1/messages.json"}
}

func (p *Pushover) Send(ctx context.Context, appToken, userKey, title, message string) error {
	if p == nil {
		return fmt.Errorf("Pushover client is not configured")
	}
	appToken = strings.TrimSpace(appToken)
	userKey = strings.TrimSpace(userKey)
	if appToken == "" {
		return fmt.Errorf("Pushover application token is empty")
	}
	if userKey == "" {
		return fmt.Errorf("Pushover user key is empty")
	}
	f := url.Values{"token": {appToken}, "user": {userKey}, "title": {title}, "message": {message}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, strings.NewReader(f.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Pushover HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var result struct {
		Status int      `json:"status"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(b, &result); err == nil && result.Status != 1 {
		return fmt.Errorf("Pushover rejected message: %s", strings.Join(result.Errors, "; "))
	}
	return nil
}
