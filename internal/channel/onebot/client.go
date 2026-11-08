package onebot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrActionRejected = errors.New("onebot action rejected")

type ActionConfig struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

type ActionClient struct {
	base  string
	token string
	http  *http.Client
}

func NewActionClient(cfg ActionConfig) (*ActionClient, error) {
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !loopbackHost(parsed.Hostname()) {
		return nil, errors.New("onebot action URL must be loopback HTTP")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("onebot action token is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	client := &http.Client{
		Timeout: cfg.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &ActionClient{base: strings.TrimRight(parsed.String(), "/"), token: cfg.Token, http: client}, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *ActionClient) SendPrivateText(ctx context.Context, targetID, text string) error {
	target, err := decimalID(targetID)
	if err != nil || strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return ErrInvalidEvent
	}
	payload := struct {
		UserID  int64 `json:"user_id"`
		Message []struct {
			Type string            `json:"type"`
			Data map[string]string `json:"data"`
		} `json:"message"`
	}{UserID: target}
	payload.Message = append(payload.Message, struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}{Type: "text", Data: map[string]string{"text": text}})
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/send_private_msg", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Status  string `json:"status"`
		Retcode int    `json:"retcode"`
		Wording string `json:"wording"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	if err := decoder.Decode(&result); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || result.Retcode != 0 || result.Status == "failed" {
		return fmt.Errorf("%w: HTTP %d retcode %d", ErrActionRejected, resp.StatusCode, result.Retcode)
	}
	return nil
}
