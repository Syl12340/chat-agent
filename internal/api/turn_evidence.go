package api

import "strings"

// Evidence proves provenance, not a keyword-based interpretation of consent.
func turnHasEvidence(inputs []string, quote string) bool {
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
