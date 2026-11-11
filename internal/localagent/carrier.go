package localagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Carrier struct {
	BaseURL string
	Device  string
	Token   string
	Client  *http.Client
}

func (c *Carrier) Validate() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("local agent server must be an HTTPS origin")
	}
	if c.Device == "" || c.Token == "" {
		return errors.New("local agent device and token are required")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 35 * time.Second}
	}
	return nil
}

func (c *Carrier) Claim(ctx context.Context) (*Task, error) {
	request := map[string]any{"protocol": Protocol, "device_id": c.Device, "capabilities": []string{"list", "read", "search", "exec", "artifact-metadata"}}
	var task Task
	status, err := c.call(ctx, http.MethodPost, "/api/local-agent/v1/tasks/claim", request, &task)
	if status == http.StatusNoContent {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (c *Carrier) Complete(ctx context.Context, receipt Receipt) error {
	_, err := c.call(ctx, http.MethodPost, "/api/local-agent/v1/tasks/"+url.PathEscape(receipt.OperationID)+"/complete", map[string]any{"device_id": c.Device, "receipt": receipt}, nil)
	return err
}

func (c *Carrier) call(ctx context.Context, method, path string, input, output any) (int, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chat-Agent-ID", c.Device)
	response, err := c.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return response.StatusCode, fmt.Errorf("local agent carrier returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if output != nil && json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output) != nil {
		return response.StatusCode, errors.New("local agent carrier returned invalid JSON")
	}
	return response.StatusCode, nil
}
