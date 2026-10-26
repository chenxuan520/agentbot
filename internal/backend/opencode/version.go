package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type authenticatedTransport struct {
	base     http.RoundTripper
	origin   *url.URL
	username string
	password string
}

func (t *authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.origin != nil && request.URL.Scheme == t.origin.Scheme && request.URL.Host == t.origin.Host {
		request = request.Clone(request.Context())
		request.SetBasicAuth(t.username, t.password)
	}
	return t.base.RoundTrip(request)
}

func (c *Client) useV2(ctx context.Context) (bool, error) {
	if !c.autoDetect {
		return c.version == 2, nil
	}
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.version != 0 {
		return c.version == 2, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/info", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.version = 1
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("opencode API detection status: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err != nil {
		return false, err
	}
	prefix := strings.ToLower(strings.TrimSpace(string(body)))
	if strings.HasPrefix(prefix, "<!doctype html") || strings.HasPrefix(prefix, "<html") {
		c.version = 1
		return false, nil
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, fmt.Errorf("decode opencode API version: %w", err)
	}
	if info.Version == "" {
		c.version = 1
		return false, nil
	}
	if strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(info.Version), "v"), "2.") {
		c.version = 2
		return true, nil
	}
	return false, fmt.Errorf("unsupported opencode API version %q", info.Version)
}
