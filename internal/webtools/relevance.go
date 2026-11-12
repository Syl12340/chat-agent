package webtools

import (
	"strings"
	"unicode"
)

// Relevance reports how much of a result page had anything to do with the query.
//
// A search engine that answers with unrelated pages is worse than one that fails:
// the caller cannot tell the difference and may repeat the noise as a finding. This
// is a cheap overlap check, not a ranking — it only has to notice when nothing
// matched at all.
type Relevance struct {
	Matched int    `json:"matched"`
	Total   int    `json:"total"`
	Warning string `json:"warning,omitempty"`
}

// irrelevantWarning is what the model sees when no result mentions the query.
const irrelevantWarning = "没有任何一条结果提到查询里的词，搜索引擎可能返回了无关内容；" +
	"不要把这些结果当作答案，也不要据此下结论。可以换个说法再搜一次，或者直接说明没查到。"

// AssessRelevance counts results that mention at least one term from the query.
//
// Latin queries are split on non-letters and kept from three characters up. CJK
// queries have no word breaks, so each run of CJK characters also contributes its
// two-character grams; a single character would match almost anything.
func AssessRelevance(query string, results []Result) Relevance {
	relevance := Relevance{Total: len(results)}
	terms := queryTerms(query)
	if len(terms) == 0 || len(results) == 0 {
		return relevance
	}
	for _, result := range results {
		haystack := strings.ToLower(result.Title + " " + result.Snippet + " " + result.URL)
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				relevance.Matched++
				break
			}
		}
	}
	if relevance.Matched == 0 {
		relevance.Warning = irrelevantWarning
	}
	return relevance
}

func queryTerms(query string) []string {
	lowered := strings.ToLower(query)
	terms := make([]string, 0, 16)
	seen := map[string]bool{}
	add := func(term string) {
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}

	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() >= 3 {
			add(latin.String())
		}
		latin.Reset()
	}
	flushCJK := func() {
		for index := 0; index+1 < len(cjk); index++ {
			add(string(cjk[index : index+2]))
		}
		cjk = cjk[:0]
	}

	for _, r := range lowered {
		switch {
		case unicode.Is(unicode.Han, r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin.WriteRune(r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return terms
}
