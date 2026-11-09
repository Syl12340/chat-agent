package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"pocket-agent/internal/imagegen"
	"pocket-agent/internal/probe"
)

// imageTools exposes local image generation as a single ordinary capability.
//
// Two boundaries are deliberate. First, the model may only pick from the bridge's
// approved profiles; it cannot name a model or supply a workflow, so the renderer
// stays a host decision. Second, the produced file lands in the session's media
// store under a host-assigned id — the model gets a reference, never a path it
// could forge.
func (s *Server) imageTools(role, sessionID string) []probe.Tool {
	if s.imagegen == nil || s.images == nil {
		return nil
	}
	// The tool stays registered even when the renderer is down: health is reported to
	// the model, never used to hide the capability. Only a missing profile list makes
	// the enum empty, and an empty enum simply means "any string", which the handler
	// still validates against the approved set.
	profiles := s.imagegen.Profiles()
	profileSchema := `"profile":{"type":"string","description":"Renderer profile. Different families draw very differently; prefer the default unless the user asks for a style."}`
	if len(profiles) > 0 {
		quoted := make([]string, len(profiles))
		for i, name := range profiles {
			quoted[i] = `"` + name + `"`
		}
		profileSchema = `"profile":{"type":"string","enum":[` + strings.Join(quoted, ",") + `],"description":"Renderer profile. Different families draw very differently; prefer the default unless the user asks for a style."}`
	}
	parameters := fmt.Sprintf(`{"type":"object","properties":{`+
		`"prompt":{"type":"string","minLength":1,"maxLength":1500,"description":"What to draw, in the style the chosen profile understands."},`+
		`"negative":{"type":"string","maxLength":1000,"description":"What to avoid."},`+
		`%s,`+
		`"width":{"type":"integer","minimum":256,"maximum":1536},`+
		`"height":{"type":"integer","minimum":256,"maximum":1536},`+
		`"steps":{"type":"integer","minimum":1,"maximum":40},`+
		`"cfg":{"type":"number","minimum":0,"maximum":30,"description":"Prompt adherence. Lower is looser and more painterly; most profiles want 4-7."},`+
		`"seed":{"type":"integer","description":"Reuse a seed to reproduce a previous result."},`+
		`"min_free_ram_gb":{"type":"number","minimum":1.5,"maximum":64,"description":"Override how much host memory must be free before this render starts. Only lower it when the user accepts a slower machine or a possible failure; omit to use the profile default."}`+
		`},"required":["prompt"],"additionalProperties":false}`, profileSchema)

	return []probe.Tool{s.imageHealthTool(), {
		Name:  "image_generate",
		Order: 660,
		Usage: "use only when the user actually asks for a picture. It draws one image on the local GPU and usually takes 10-60 seconds, so do not call it speculatively, and never call it twice for the same request. Give a concrete visual description in the language the user used. The size must respect the profile: image_health lists each profile's width/height range and alignment, so check it when the user asks for a specific shape. If the renderer refuses for lack of memory, say how much it needed instead of retrying; lowering min_free_ram_gb is only right when the user accepts a slower machine. When it succeeds, tell the user what you drew in one short sentence; the picture appears by itself, so do not paste URLs or describe the file.",
		Description: "Draw an image locally from a text description and attach it to this conversation. " +
			"Returns an image reference plus the profile and seed that produced it. " +
			"Only the host-approved renderer profiles are available; they are different model families, so results look different. " +
			"If the renderer is busy the call fails with a clear message — report that plainly instead of retrying repeatedly. Memory pressure is reported separately by image_health: on a machine with little free RAM the first load of a model pages through disk, which makes the first picture slow without meaning it failed.",
		Parameters: json.RawMessage(parameters),
		Timeout:    4 * 60 * 1e9,
		Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			var args struct {
				Prompt   string `json:"prompt"`
				Negative string `json:"negative"`
				Profile  string `json:"profile"`
				Width    int    `json:"width"`
				Height   int    `json:"height"`
				Steps    int    `json:"steps"`
				Seed     int64  `json:"seed"`
				CFG           float64 `json:"cfg"`
				MinFreeRAMGB  float64 `json:"min_free_ram_gb"`
			}
			if err := toolInput(data, &args); err != nil {
				return probe.ToolResult{}, err
			}
			result, err := s.imagegen.Generate(ctx, imagegen.Request{
				Profile:  args.Profile,
				Prompt:   args.Prompt,
				Negative: args.Negative,
				Width:    args.Width,
				Height:   args.Height,
				Steps:    args.Steps,
				Seed:     args.Seed,
				CFG:      args.CFG,
				MinFreeRAMGB: args.MinFreeRAMGB,
			})
			if err != nil {
				return probe.ToolResult{}, err
			}
			if len(result.Images) == 0 {
				return probe.ToolResult{}, errors.New("图像生成没有产出图片")
			}
			first := result.Images[0]
			item, err := s.images.Upload(role, sessionID, bytes.NewReader(first.Data))
			if err != nil {
				return probe.ToolResult{}, errors.New("生成的图片无法保存到会话")
			}
			out, err := json.Marshal(struct {
				ImageID string `json:"image_id"`
				URL     string `json:"url"`
				Profile string `json:"profile"`
				Seed    int64  `json:"seed"`
				Note    string `json:"note"`
			}{
				ImageID: item.ID,
				URL:     fmt.Sprintf("/api/sessions/%s/images/%s", sessionID, item.ID),
				Profile: result.Profile,
				Seed:    result.Seed,
				Note:    "图片已附加到本会话，用户可以直接看到；不要粘贴这个 URL。",
			})
			if err != nil {
				return probe.ToolResult{}, errors.New("图像结果无法编码")
			}
			return probe.ToolResult{Output: string(out)}, nil
		},
	}}
}
