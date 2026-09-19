// Package client 与服务端通信。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"token-monitor-client/internal/collector"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail) }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "token-monitor-client")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Detail == "" {
			e.Detail = string(bytes.TrimSpace(data))
		}
		if len(e.Detail) > 300 {
			e.Detail = e.Detail[:300]
		}
		return &APIError{Status: resp.StatusCode, Detail: e.Detail}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

type DeviceInfo struct {
	DeviceID        string `json:"device_id"`
	Name            string `json:"name"`
	OS              string `json:"os"`
	Hostname        string `json:"hostname"`
	ClientVersion   string `json:"client_version"`
	IntervalMinutes int    `json:"interval_minutes"`
	Agents          any    `json:"agents"`
	Status          any    `json:"status"`
}

type RegisterResp struct {
	OK            bool   `json:"ok"`
	DeviceID      string `json:"device_id"`
	ServerVersion string `json:"server_version"`
	ServerTime    string `json:"server_time"`
}

func (c *Client) Register(ctx context.Context, d DeviceInfo) (RegisterResp, error) {
	var r RegisterResp
	err := c.do(ctx, http.MethodPost, "/api/v1/devices/register", d, &r)
	return r, err
}

func (c *Client) Heartbeat(ctx context.Context, d DeviceInfo) error {
	return c.do(ctx, http.MethodPost, "/api/v1/devices/heartbeat", d, nil)
}

type IngestReq struct {
	DeviceID string            `json:"device_id"`
	Agent    string            `json:"agent"`
	Batches  []collector.Batch `json:"batches"`
}

type IngestResp struct {
	UsageRows   int `json:"usage_rows"`
	SessionRows int `json:"session_rows"`
	Skipped     int `json:"skipped"`
}

func (c *Client) Ingest(ctx context.Context, req IngestReq) (IngestResp, error) {
	var r IngestResp
	err := c.do(ctx, http.MethodPost, "/api/v1/ingest", req, &r)
	return r, err
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Devices int    `json:"devices"`
	Online  int    `json:"online"`
	Now     string `json:"now"`
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/api/health", nil, &h)
	return h, err
}

type PricingItem struct {
	Agent          string  `json:"agent"`
	Model          string  `json:"model"`
	Provider       string  `json:"provider,omitempty"`
	InputPerM      float64 `json:"input_per_m"`
	OutputPerM     float64 `json:"output_per_m"`
	CacheReadPerM  float64 `json:"cache_read_per_m"`
	CacheWritePerM float64 `json:"cache_write_per_m"`
	Configured     bool    `json:"configured"`
}

func (c *Client) GetPricing(ctx context.Context, deviceID string) ([]PricingItem, error) {
	var r struct {
		Pricing []PricingItem `json:"pricing"`
	}
	path := "/api/v1/pricing"
	if deviceID != "" {
		path += "?device_id=" + deviceID
	}
	err := c.do(ctx, http.MethodGet, path, nil, &r)
	return r.Pricing, err
}

func (c *Client) PutPricing(ctx context.Context, deviceID string, items []PricingItem) error {
	return c.do(ctx, http.MethodPut, "/api/v1/pricing", map[string]any{"device_id": deviceID, "items": items}, nil)
}

// GetDeviceConfig 读取服务端保存的设备配置（配置以服务端为准）；设备从未推送过时返回 nil。
func (c *Client) GetDeviceConfig(ctx context.Context, deviceID string) (json.RawMessage, error) {
	var r struct {
		Config json.RawMessage `json:"config"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/devices/"+deviceID+"/config", nil, &r)
	return r.Config, err
}

// PutDeviceConfig 把整份配置快照存到服务端（服务端只存不解释）
func (c *Client) PutDeviceConfig(ctx context.Context, deviceID string, cfg any) error {
	return c.do(ctx, http.MethodPut, "/api/v1/devices/"+deviceID+"/config", cfg, nil)
}
