package probe

import (
	"fmt"
	"sort"
	"strings"
)

// A prompt section is one named, ordered block. DeepSeek Harness (MIT) composes
// its system prompt the same way: plugins register sections with an order, and
// assembly sorts them, so the prompt stays auditable instead of accumulating as
// string concatenation.
type PromptSection struct {
	Name  string
	Order int
	Text  string
}

type Prompt struct {
	sections []PromptSection
	err      error
}

func NewPrompt() *Prompt { return &Prompt{} }

// Add registers a section in one layer. Duplicate names fail assembly instead
// of silently overriding policy. Use Override for an intentional replacement.
func (p *Prompt) Add(name string, order int, text string) {
	if name == "" {
		p.err = fmt.Errorf("prompt section needs a name")
		return
	}
	for i := range p.sections {
		if p.sections[i].Name == name {
			p.err = fmt.Errorf("duplicate prompt section %q", name)
			return
		}
	}
	p.sections = append(p.sections, PromptSection{Name: name, Order: order, Text: text})
}

func (p *Prompt) Override(name string, order int, text string) {
	for i := range p.sections {
		if p.sections[i].Name == name {
			p.sections[i] = PromptSection{Name: name, Order: order, Text: text}
			return
		}
	}
	p.err = fmt.Errorf("cannot override missing prompt section %q", name)
}

func (p *Prompt) Render() (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.String(), nil
}

// String renders the non-empty sections in stable order.
func (p *Prompt) String() string {
	ordered := append([]PromptSection(nil), p.sections...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Order == ordered[j].Order {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].Order < ordered[j].Order
	})
	var out strings.Builder
	for _, section := range ordered {
		text := strings.TrimSpace(section.Text)
		if text == "" {
			continue
		}
		out.WriteString(text)
		out.WriteString("\n")
	}
	return out.String()
}
