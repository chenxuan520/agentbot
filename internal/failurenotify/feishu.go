package failurenotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type FeishuWebhook struct {
	webhookURL string
	httpClient *http.Client

	mu       sync.Mutex
	lastSent map[string]time.Time
}

func NewFeishuWebhook(webhookURL string) *FeishuWebhook {
	return &FeishuWebhook{
		webhookURL: strings.TrimSpace(webhookURL),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		lastSent:   map[string]time.Time{},
	}
}

func (n *FeishuWebhook) Enabled() bool {
	return n != nil && n.webhookURL != ""
}

func (n *FeishuWebhook) NotifyText(ctx context.Context, text string) error {
	if !n.Enabled() {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := n.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu webhook status: %s", resp.Status)
	}
	return nil
}

func (n *FeishuWebhook) NotifyTextThrottled(ctx context.Context, key, text string, cooldown time.Duration) error {
	if !n.Enabled() {
		return nil
	}
	if cooldown <= 0 {
		return n.NotifyText(ctx, text)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return n.NotifyText(ctx, text)
	}
	n.mu.Lock()
	last, ok := n.lastSent[key]
	n.mu.Unlock()
	if ok && time.Since(last) < cooldown {
		return nil
	}
	if err := n.NotifyText(ctx, text); err != nil {
		return err
	}
	n.mu.Lock()
	n.lastSent[key] = time.Now()
	n.mu.Unlock()
	return nil
}
