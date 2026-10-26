package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type v2HTTPError struct {
	Method     string
	Path       string
	Status     int
	StatusText string
}

func (e *v2HTTPError) Error() string {
	return fmt.Sprintf("opencode V2 %s %s status: %s", e.Method, e.Path, e.StatusText)
}

func (c *Client) v2Request(ctx context.Context, method, path string, query url.Values, body, result any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	address := c.baseURL + path
	if len(query) != 0 {
		address += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &v2HTTPError{Method: method, Path: path, Status: resp.StatusCode, StatusText: resp.Status}
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode opencode V2 %s: %w", path, err)
		}
	}
	return nil
}

func v2LocationQuery(workspacePath string) url.Values {
	query := url.Values{}
	if workspacePath != "" {
		query.Set("location[directory]", workspacePath)
	}
	return query
}
