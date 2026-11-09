// Command imagebridge exposes the local image-generation stack over one HTTP
// endpoint, so a server without a GPU can use it through a tunnel.
//
// Why a bridge instead of forwarding ComfyUI's port: the generation stack is
// imgctl.py driving a specific workflow, not a raw ComfyUI API. Forwarding 8188
// would make the server speak ComfyUI's prompt graph; this way it speaks one
// small, stable request shape and the workflow stays on the machine with the GPU.
//
//	[server] chat-agent ──HTTP──▶ 127.0.0.1:18190 (reverse tunnel)
//	                                    │
//	                              imagebridge (this)
//	                                    │
//	                              imgctl.py ──▶ ComfyUI 127.0.0.1:8188
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"pocket-agent/internal/imagegen"
)

const version = "1.1.0"

type service struct {
	client *imagegen.Client
	token  string
	start  time.Time
}

func main() {
	var (
		listen   = flag.String("listen", envOr("IMAGEBRIDGE_LISTEN", "127.0.0.1:18190"), "监听地址，默认只绑本机")
		python   = flag.String("python", envOr("IMAGEBRIDGE_PYTHON", ""), "调用桥的 Python 解释器")
		script   = flag.String("script", envOr("IMAGEBRIDGE_SCRIPT", ""), "imgctl.py 路径")
		profiles = flag.String("profiles", envOr("IMAGEBRIDGE_PROFILES", ""), "允许的 profile，逗号分隔；空表示不限制")
		fallback = flag.String("default", envOr("IMAGEBRIDGE_PROFILE", ""), "默认 profile")
		token    = flag.String("token", envOr("IMAGEBRIDGE_TOKEN", ""), "可选的共享密钥；设置后请求需带 Bearer")
	)
	flag.Parse()

	if *python == "" || *script == "" {
		log.Fatal("必须指定 -python 与 -script（或 IMAGEBRIDGE_PYTHON / IMAGEBRIDGE_SCRIPT）")
	}
	client := imagegen.New(imagegen.Config{
		Python:         *python,
		Script:         *script,
		Profiles:       splitList(*profiles),
		DefaultProfile: *fallback,
		// This bridge is the trusted on-device side: it exposes whatever the
		// renderer reports rather than a fixed allow-list.
		AllowAllProfiles: true,
	})
	if client == nil {
		log.Fatal("图像生成客户端未就绪，检查 -python 与 -script")
	}

	// Refuse to listen on anything but loopback unless the operator insists: this
	// service can spend GPU time, so it must not be reachable from the LAN by
	// accident. The tunnel is the intended path.
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("监听地址无效：%v", err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && *token == "" {
		log.Fatal("绑定非本机地址时必须设置 -token，否则任何人都能消耗显卡")
	}

	svc := &service{client: client, token: strings.TrimSpace(*token), start: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", svc.health)
	mux.HandleFunc("/profiles", svc.profileList)
	mux.HandleFunc("/generate", svc.generate)
	mux.HandleFunc("POST /jobs", svc.submitJob)
	mux.HandleFunc("GET /jobs/{job}/result", svc.waitJob)
	mux.HandleFunc("POST /jobs/{job}/cancel", svc.cancelJob)

	server := &http.Server{
		Addr:              *listen,
		Handler:           svc.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// Generation is slow by nature; the client enforces its own deadline.
		WriteTimeout: 6 * time.Minute,
		IdleTimeout:  90 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	log.Printf("imagebridge %s listening on %s", version, *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// guard applies the shared secret and a few conservative response headers.
func (s *service) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if s.token != "" {
			presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if strings.TrimSpace(presented) != s.token {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "未授权", "unavailable": false})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// health answers with a single flattened snapshot. The server treats ok=false as
// "capability unavailable" and shows the reason to the user, so the reason has to
// be written for a person.
func (s *service) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	health, err := s.client.Health(ctx)
	if err != nil {
		reason := "图像生成服务没有响应"
		if errors.Is(err, imagegen.ErrUnavailable) {
			reason = "图像生成服务未就绪"
		} else if err.Error() != "" {
			reason = err.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "comfy": false, "reason": reason, "version": version,
		})
		return
	}

	profiles, profileErr := s.client.AvailableProfiles(ctx)
	// Split by what can actually run right now. "ready" only means the models are on
	// disk; the renderer also refuses a job when host memory is below the profile's
	// floor, and that refusal is the most common failure. Reporting it as data lets
	// the model choose a lighter profile or explain the wait.
	// committable is what the system can still hand out (physical + page file).
	// It is the ceiling that matters, not physical free memory alone: ComfyUI
	// swaps models out and Windows pages, so the physical figure understates it.
	committable := health.Memory.PagefileAvailableGB
	if committable <= 0 {
		committable = health.Memory.AvailableGB
	}
	ready := make([]string, 0, len(profiles))
	blocked := make([]map[string]any, 0)
	// Specs let the model pick a size that is actually legal for the profile it
	// chose, instead of guessing and being rejected.
	specs := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		specs = append(specs, map[string]any{
			"name":            profile.Name,
			"mode":            profile.Mode,
			"width":           profile.Dimensions.Width,
			"height":          profile.Dimensions.Height,
			"alignment":       profile.Dimensions.Alignment,
			"min_free_ram_gb": profile.MinFreeRAMGB,
			"ready":           profile.Ready,
		})
		switch {
		case !profile.Ready:
			reason := "缺少模型文件"
			if len(profile.MissingAssets) > 0 {
				reason = "缺少 " + strings.Join(profile.MissingAssets, "、")
			}
			blocked = append(blocked, map[string]any{"name": profile.Name, "reason": reason})
		case profile.MinFreeRAMGB > 0 && committable < profile.MinFreeRAMGB:
			// Only stop here when the system genuinely cannot commit the memory. ComfyUI
			// swaps models itself, so physical free memory alone is not disqualifying.
			blocked = append(blocked, map[string]any{
				"name": profile.Name,
				"reason": fmt.Sprintf("可提交内存不足：需要 %.1fGB，当前 %.1fGB",
					profile.MinFreeRAMGB, committable),
				"min_free_ram_gb": profile.MinFreeRAMGB,
			})
		// Physical memory pressure is a property of the machine, not of each profile,
		// so it is reported once at the top level instead of repeated per entry.

		default:
			ready = append(ready, profile.Name)
		}
	}
	ok := health.ComfyOnline && profileErr == nil && len(ready) > 0
	reason := ""
	switch {
	case !health.ComfyOnline:
		reason = "ComfyUI 没有运行"
	case profileErr != nil:
		reason = "无法读取可用的生成配置"
	case len(ready) == 0:
		reason = "当前没有能跑的生成配置（内存不足或模型缺失）"
	}
	writeJSON(w, statusFor(ok), map[string]any{
		"ok":       ok,
		"comfy":    health.ComfyOnline,
		"reason":   reason,
		"profiles": ready,
		"blocked":  blocked,
		"specs":    specs,
		"default":  s.client.DefaultProfile(),
		"version":  version,
		"bridge":   health.BridgeVersion,
		"queue": map[string]any{
			"running": len(health.Queue.Running),
			"pending": len(health.Queue.Pending),
		},
		"gpu": map[string]any{
			"name":       health.GPU.Name,
			"vram_total": health.GPU.TotalMB,
			"vram_used":  health.GPU.UsedMB,
			"vram_free":  health.GPU.FreeMB,
			"temp_c":     health.GPU.TempC,
		},
		"memory_pressure": memoryPressure(health.Memory.AvailableGB, committable),
		"memory": map[string]any{
			"load_percent":   health.Memory.LoadPercent,
			"total_gb":       health.Memory.TotalGB,
			"available_gb":   health.Memory.AvailableGB,
			"committable_gb": committable,
		},
		"uptime_seconds": int(time.Since(s.start).Seconds()),
	})
}

func (s *service) profileList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	profiles, err := s.client.AvailableProfiles(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "无法读取生成配置"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

type generateRequest struct {
	Prompt   string  `json:"prompt"`
	Negative string  `json:"negative"`
	Profile  string  `json:"profile"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Steps    int     `json:"steps"`
	Seed     int64   `json:"seed"`
	CFG      float64 `json:"cfg"`
	// MinFreeRAMGB lets the caller lower (or raise) the profile's memory floor.
	// The Python bridge clamps it to its own hard minimum, so this can weaken a
	// refusal but never disable the check.
	MinFreeRAMGB float64 `json:"min_free_ram_gb"`
}

func (req generateRequest) request() imagegen.Request {
	return imagegen.Request{
		Profile: req.Profile, Prompt: req.Prompt, Negative: req.Negative,
		Width: req.Width, Height: req.Height, Steps: req.Steps, Seed: req.Seed,
		CFG: req.CFG, MinFreeRAMGB: req.MinFreeRAMGB,
	}
}

// generate runs one job and returns the images inline as base64. The images are
// small enough (≤1536px PNG) that a separate download step would only add
// failure modes to a link that already crosses a tunnel.
func (s *service) generate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
		return
	}
	var req generateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不正确"})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 prompt"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()

	started := time.Now()
	result, err := s.client.Generate(ctx, imagegen.Request{
		Profile:      req.Profile,
		Prompt:       req.Prompt,
		Negative:     req.Negative,
		Width:        req.Width,
		Height:       req.Height,
		Steps:        req.Steps,
		Seed:         req.Seed,
		CFG:          req.CFG,
		MinFreeRAMGB: req.MinFreeRAMGB,
	})
	if err != nil {
		unavailable := errors.Is(err, imagegen.ErrUnavailable)
		status := http.StatusBadGateway
		if unavailable {
			status = http.StatusServiceUnavailable
		}
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeJSON(w, status, map[string]any{
			"error": err.Error(), "unavailable": unavailable,
		})
		return
	}

	images := make([]map[string]any, 0, len(result.Images))
	for _, image := range result.Images {
		mime := image.MIME
		if mime == "" {
			mime = "image/png"
		}
		images = append(images, map[string]any{
			"mime":   mime,
			"base64": base64.StdEncoding.EncodeToString(image.Data),
		})
	}
	log.Printf("generate profile=%s seed=%d images=%d elapsed=%s",
		result.Profile, result.Seed, len(images), time.Since(started).Round(time.Second))
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":  result.JobID,
		"profile": result.Profile,
		"seed":    result.Seed,
		"images":  images,
	})
}

// memoryPressure describes the machine in one word, so the caller can mention it
// once instead of tagging every profile. ComfyUI swaps models itself, so this is
// advisory: "high" means the first load pages through disk, not that a run fails.
func memoryPressure(available, committable float64) string {
	switch {
	case committable > 0 && committable < 2:
		return "critical"
	case available < 4:
		return "high"
	case available < 8:
		return "moderate"
	default:
		return "low"
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func statusFor(ok bool) int {
	if ok {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

func splitList(value string) []string {
	out := []string{}
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
