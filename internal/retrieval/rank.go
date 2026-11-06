// Package retrieval ranks bounded literal references without model calls or
// persisted indexes. Scores express lexical overlap, not semantic certainty.
package retrieval

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Document struct {
	ID        string
	Text      string
	UpdatedAt time.Time
}

type Match struct {
	Index int
	Score float64
}

// Function words are ignored for retrieval only. This list never selects an
// action, changes tools, or interprets user permission.
var filler = func() map[string]bool {
	words := strings.Fields(`今天 现在 最近 这个 那个 这些 那些 怎么 什么 我们 你们 他们 可以 不能 能不能 然后 后来 接着 下去 上次 之前 刚才 刚刚 就是 还是 已经 一下 一些 一个 事情 好像 觉得 知道 记得 继续 聊聊 聊天 陪我 谢谢 想聊 想想 说说 听听 这样 那样 自己 时候 一起 还有 其实 真的 有点 the and i you me we they a an is are was were be been it this that these those my your our their to of on in for with about today now please remember continue chat talk tell thanks thank what how why can could would should do does did have has had just really again later before earlier next`)
	out := make(map[string]bool, len(words))
	for _, word := range words {
		out[word] = true
	}
	return out
}()

type term struct {
	text  string
	latin bool
}

var hanFiller = func() *strings.Replacer {
	words := []string{}
	for word := range filler {
		if unicode.Is(unicode.Han, []rune(word)[0]) {
			words = append(words, word)
		}
	}
	sort.Slice(words, func(i, j int) bool {
		if len(words[i]) != len(words[j]) {
			return len(words[i]) > len(words[j])
		}
		return words[i] < words[j]
	})
	pairs := make([]string, 0, len(words)*2)
	for _, word := range words {
		pairs = append(pairs, word, " ")
	}
	return strings.NewReplacer(pairs...)
}()

func bounded(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	// Preserve both the original subject and the latest correction in long input.
	return string(runes[:limit/2]) + " " + string(runes[len(runes)-limit/2:])
}

func queryTerms(query string) []term {
	query = hanFiller.Replace(strings.ToLower(bounded(query, 512)))
	out := []term{}
	seen := map[string]bool{}
	add := func(text string, latin bool) {
		if text != "" && !filler[text] && !seen[text] {
			seen[text] = true
			out = append(out, term{text, latin})
		}
	}
	var word []rune
	var prior rune
	flush := func() {
		if len(word) > 0 {
			add(string(word), true)
			word = nil
		}
	}
	for _, r := range query {
		if unicode.Is(unicode.Han, r) {
			flush()
			// Location-bearing grams such as 住在/在家 must survive. Whole
			// filler phrases (e.g. 现在) were removed before this scan.
			if prior != 0 && !strings.ContainsRune("我你他她它这那的了呢啊吧和是也都就很吗么个", prior) && !strings.ContainsRune("我你他她它这那的了呢啊吧和是也都就很吗么个", r) {
				add(string([]rune{prior, r}), false)
			}
			prior = r
		} else {
			prior = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '+' || r == '#' {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	// A single concrete Han query, e.g. 茶, is useful in explicit memory search.
	trimmed := []rune(strings.TrimSpace(query))
	if len(trimmed) == 1 && unicode.Is(unicode.Han, trimmed[0]) && !strings.ContainsRune("我你他她它这那的了呢啊吧和是在也都就很吗么个好", trimmed[0]) {
		add(string(trimmed), false)
	}
	if len(out) > 128 {
		out = append(out[:64:64], out[len(out)-64:]...)
	}
	return out
}

// HasTerms reports whether a query has a concrete lexical signal. It is used
// only to choose a retrieval query; it never selects tools, permissions or a
// conversational mode.
func HasTerms(query string) bool {
	return len(queryTerms(strings.TrimSpace(query))) > 0
}

func latinWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return unicode.Is(unicode.Han, r) || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '+' || r == '#')
	}) {
		out[word] = true
	}
	return out
}

// Rank returns original document indexes. A nonempty query must match a
// non-filler term; no recency fallback can make an unrelated record relevant.
// An explicitly empty query returns recent records for management workflows.
func Rank(query string, documents []Document, limit int) []Match {
	if limit < 1 {
		return []Match{}
	}
	documents = documents[:min(len(documents), 2000)]
	query = strings.TrimSpace(query)
	terms := queryTerms(query)
	if query != "" && len(terms) == 0 {
		return []Match{}
	}
	texts := make([]string, len(documents))
	words := make([]map[string]bool, len(documents))
	frequency := make([]int, len(terms))
	for i, document := range documents {
		texts[i] = strings.ToLower(bounded(document.Text, 4096))
		words[i] = latinWords(texts[i])
		for j, t := range terms {
			if (!t.latin && strings.Contains(texts[i], t.text)) || (t.latin && words[i][t.text]) {
				frequency[j]++
			}
		}
	}
	matches := []Match{}
	phrase := strings.ToLower(bounded(query, 128))
	for i := range documents {
		score := 0.0
		for j, t := range terms {
			if (!t.latin && strings.Contains(texts[i], t.text)) || (t.latin && words[i][t.text]) {
				score += 1 + math.Log(1+float64(len(documents))/float64(1+frequency[j]))
			}
		}
		if query == "" || score > 0 {
			if score > 0 && len([]rune(phrase)) > 1 && strings.Contains(texts[i], phrase) {
				score += 2
			}
			matches = append(matches, Match{Index: i, Score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if !documents[a.Index].UpdatedAt.Equal(documents[b.Index].UpdatedAt) {
			return documents[a.Index].UpdatedAt.After(documents[b.Index].UpdatedAt)
		}
		if documents[a.Index].ID != documents[b.Index].ID {
			return documents[a.Index].ID < documents[b.Index].ID
		}
		return a.Index < b.Index
	})
	return matches[:min(limit, len(matches))]
}
