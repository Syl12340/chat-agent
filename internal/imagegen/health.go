package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// BridgeHealth is the subset of the bridge's `health` reply this project acts on.
// The bridge reports a lot more (ComfyUI package versions, argv, queue); ignoring
// the rest keeps this side stable when the bridge grows fields.
type BridgeHealth struct {
	BridgeVersion string `json:"bridge_version"`
	ComfyURL      string `json:"comfy_url"`
	ComfyOnline   bool   `json:"comfy_online"`
	GPU           struct {
		Name      string `json:"name"`
		TotalMB   int    `json:"vram_total_mb"`
		UsedMB    int    `json:"vram_used_mb"`
		FreeMB    int    `json:"vram_free_mb"`
		TempC     int    `json:"temperature_c"`
		Utilized  int    `json:"utilization_percent"`
	} `json:"gpu"`
	Memory struct {
		LoadPercent int     `json:"load_percent"`
		TotalGB     float64 `json:"total_gb"`
		AvailableGB float64 `json:"available_gb"`
		// PagefileAvailableGB is what Windows can still commit, including the page
		// file. It is the real ceiling for loading weights, unlike physical free RAM.
		PagefileAvailableGB float64 `json:"pagefile_available_gb"`
	} `json:"memory"`
	Queue struct {
		Running []any `json:"queue_running"`
		Pending []any `json:"queue_pending"`
	} `json:"queue"`
}

// Busy reports whether the local ComfyUI queue has work in flight.
func (h *BridgeHealth) Busy() bool {
	if h == nil {
		return false
	}
	return len(h.Queue.Running) > 0 || len(h.Queue.Pending) > 0
}

// Health probes the bridge CLI. A missing interpreter, a stopped ComfyUI or a
// timeout all come back as an error; callers treat that as "not available".
func (c *Client) Health(ctx context.Context) (*BridgeHealth, error) {
	if c == nil {
		return nil, ErrUnavailable
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	stdout, err := c.run(ctx, []string{"health"})
	payload := extractJSON(stdout)
	if payload == nil {
		if ctx.Err() != nil {
			return nil, errors.New("图像生成服务响应超时")
		}
		return nil, ErrUnavailable
	}
	var health BridgeHealth
	if err := json.Unmarshal(payload, &health); err != nil {
		return nil, errors.New("图像生成服务的健康信息无法解析")
	}
	if err != nil && !health.ComfyOnline {
		return &health, ErrUnavailable
	}
	return &health, nil
}

// ProfileInfo is one entry from the bridge's `profiles` reply.
type ProfileInfo struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Mode     string `json:"mode"`
	Workflow string `json:"workflow"`
	Ready    bool   `json:"ready"`
	// MissingAssets names workflow files or checkpoints that are absent.
	MissingAssets []string `json:"missing_assets"`
	// MinFreeRAMGB is the host-memory floor the renderer enforces before it will
	// accept a job. Reporting it lets the model explain a refusal instead of
	// retrying blindly.
	MinFreeRAMGB float64 `json:"min_free_ram_gb"`
	Dimensions   struct {
		Width     []int `json:"width"`
		Height    []int `json:"height"`
		Alignment int   `json:"alignment"`
	} `json:"dimensions"`
}

// AvailableProfiles asks the bridge which profiles it can actually run right now,
// which is not the same as the configured allow-list: a profile whose checkpoint is
// missing reports ready=false.
func (c *Client) AvailableProfiles(ctx context.Context) ([]ProfileInfo, error) {
	if c == nil {
		return nil, ErrUnavailable
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	stdout, err := c.run(ctx, []string{"profiles"})
	payload := extractJSON(stdout)
	if payload == nil {
		return nil, ErrUnavailable
	}
	var reply struct {
		Profiles []ProfileInfo `json:"profiles"`
	}
	if err := json.Unmarshal(payload, &reply); err != nil {
		return nil, errors.New("图像生成配置无法解析")
	}
	if err != nil {
		return nil, err
	}
	return reply.Profiles, nil
}

// Status reports what the local bridge can do right now. Like the remote version it
// is advisory: the host keeps the tool registered even when Ready is false, and the
// model decides what to do with the answer.
func (c *Client) Status(ctx context.Context) Status {
	if c == nil {
		return Status{Reason: "未配置图像生成服务"}
	}
	status := Status{Profiles: c.Profiles(), Default: c.fallback}
	health, err := c.Health(ctx)
	if err != nil {
		status.Reason = "图像生成服务没有响应"
		if err.Error() != "" {
			status.Reason = err.Error()
		}
		return status
	}
	available, profileErr := c.AvailableProfiles(ctx)
	if profileErr == nil {
		ready := make([]string, 0, len(available))
		for _, profile := range available {
			if profile.Ready {
				ready = append(ready, profile.Name)
			}
		}
		if len(ready) > 0 {
			status.Profiles = ready
		}
	}
	status.Ready = health.ComfyOnline && len(status.Profiles) > 0
	switch {
	case !health.ComfyOnline:
		status.Reason = "ComfyUI 没有运行"
	case len(status.Profiles) == 0:
		status.Reason = "没有可用的生成配置"
	}
	status.Details = map[string]any{
		"bridge": health.BridgeVersion,
		"comfy":  health.ComfyOnline,
		"queue": map[string]any{
			"running": len(health.Queue.Running),
			"pending": len(health.Queue.Pending),
		},
		"gpu": map[string]any{
			"name":       health.GPU.Name,
			"vram_total": health.GPU.TotalMB,
			"vram_used":  health.GPU.UsedMB,
			"temp_c":     health.GPU.TempC,
		},
		"memory": map[string]any{
			"load_percent": health.Memory.LoadPercent,
			"available_gb": health.Memory.AvailableGB,
			"committable_gb": health.Memory.PagefileAvailableGB,
		},
	}
	return status
}
