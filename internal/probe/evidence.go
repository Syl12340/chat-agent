package probe

import "strings"

// TurnHasEvidence reports whether quote really appears in one of the user
// messages that started this run.
//
// It proves provenance, not a keyword-based reading of consent: a tool may only
// claim the user asked for something by handing back text the user actually
// typed. Inputs come from ToolInputs, so a paraphrase, a hallucinated quote or a
// sentence lifted from an assistant turn all fail here.
func TurnHasEvidence(inputs []string, quote string) bool {
	quote = strings.TrimSpace(quote)
	if quote == "" || len(quote) > 8192 {
		return false
	}
	for _, input := range inputs {
		if strings.Contains(input, quote) {
			return true
		}
	}
	return false
}
