package webtools

import (
	"net/url"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// A page used to reach the model as one flattened string: tags removed, all
// whitespace collapsed, structure gone. What a reader actually needs in order to
// decide where to go next - the headings, the links with their text, the tables
// with their rows - was exactly what got thrown away, so a model could quote a
// page but not explore it.
//
// The page keeps those things, bounded, and every link and table carries a
// reference that a later call can act on. References are positional within one
// parse of one revision, which is why the page carries a revision: an old
// reference must be refused rather than silently resolved against a new page.
const (
	maxLinks     = 120
	maxHeadings  = 60
	maxTables    = 6
	maxTableRows = 40
	maxTableCols = 8
	maxContext   = 96
)

// Page is one fetched document, with the structure a next step can use.
type Page struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	// Content is the readable text, still what most answers quote from.
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`

	// PageID and Revision identify this observation. A reference taken from here
	// is only valid while the revision matches.
	PageID   string `json:"page_id,omitempty"`
	Revision int    `json:"revision,omitempty"`

	Kind     string `json:"kind"`
	Parse    string `json:"parse_quality"`
	Bytes    int    `json:"bytes"`
	TextBytes int   `json:"text_bytes"`

	Headings []Heading `json:"headings,omitempty"`
	Links    []Link    `json:"links,omitempty"`
	Tables   []Table   `json:"tables,omitempty"`
	// LinkCount and TableCount are the totals before the bounds were applied, so a
	// model can tell "this page has twelve links" from "this page has two".
	LinkCount  int `json:"link_count,omitempty"`
	TableCount int `json:"table_count,omitempty"`
}

// Heading is one title in the document outline.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Link is an anchor with the text a person would read and enough surroundings to
// tell two similar links apart.
type Link struct {
	Ref     string `json:"ref"`
	Text    string `json:"text"`
	URL     string `json:"url,omitempty"`
	Context string `json:"context,omitempty"`
}

// Table is a rendered table, kept as rows because that is what a model can reason
// about and what a spreadsheet export needs.
type Table struct {
	Ref     string     `json:"ref"`
	Caption string     `json:"caption,omitempty"`
	Rows    [][]string `json:"rows"`
	// Truncated reports that the table was cut to the row or column bound.
	Truncated bool `json:"truncated,omitempty"`
}

// The page kinds a caller can branch on. They are extraction hints, not a domain
// allowlist: a site-specific adapter may improve one, never gate access.
const (
	KindArticle = "article"
	KindIndex   = "index"
	KindTable   = "table"
	KindEmpty   = "empty"
)

var skipSubtree = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"nav": true, "footer": true, "aside": true, "form": true, "svg": true,
}

var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "header": true,
	"li": true, "tr": true, "br": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "blockquote": true, "pre": true,
	"dd": true, "dt": true, "figcaption": true, "td": true, "th": true,
}

// ParsePage turns one response body into the page object. base is the address the
// document was served from, which is the only legitimate way to resolve a
// relative link; an HTML base tag is treated as data to check, not as authority.
func ParsePage(base string, raw []byte, truncated bool) Page {
	page := Page{URL: base, Bytes: len(raw), Parse: "good"}
	root, err := xhtml.Parse(strings.NewReader(string(raw)))
	if err != nil {
		page.Parse, page.Kind = "partial", KindEmpty
		page.Content = collapseWhitespace(string(raw))
		page.TextBytes = len(page.Content)
		return page
	}
	page.Title = documentTitle(root)
	outline := make([]Heading, 0, 16)
	links := make([]Link, 0, 32)
	tables := make([]Table, 0, 4)
	var text strings.Builder
	walk(root, page.URL, &text, &outline, &links, &tables)

	page.Content = tidyMarkdown(text.String())
	page.TextBytes = len(page.Content)
	page.Headings = boundedHeadings(outline)
	page.LinkCount = len(links)
	page.Links = boundedLinks(links)
	page.TableCount = len(tables)
	page.Tables = boundedTables(tables)
	page.Truncated = truncated
	page.Kind = classifyPage(page)
	if page.Content == "" {
		page.Parse = "empty"
	}
	return page
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// tidyMarkdown trims runaway blank lines without flattening the structure the
// renderer just built. Collapsing all whitespace here would undo the headings,
// list items and table rows that make the text navigable.
func tidyMarkdown(value string) string {
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func documentTitle(root *xhtml.Node) string {
	var title string
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if title != "" {
			return
		}
		if node.Type == xhtml.ElementNode && node.Data == "title" {
			title = collapseWhitespace(nodeText(node))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return clip(title, maxContext*2)
}

func nodeText(node *xhtml.Node) string {
	var out strings.Builder
	var visit func(*xhtml.Node)
	visit = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			out.WriteString(n.Data)
			out.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return out.String()
}

func attr(node *xhtml.Node, name string) string {
	for _, item := range node.Attr {
		if item.Key == name {
			return item.Val
		}
	}
	return ""
}

// walk renders the document as markdown while collecting the outline, links and
// tables. Markdown rather than flattened text because a reader has to be able to
// tell a heading from a paragraph and a link from the words around it; stripping
// the tags removed exactly that.
func walk(node *xhtml.Node, base string, text *strings.Builder, outline *[]Heading, links *[]Link, tables *[]Table) {
	if node.Type == xhtml.ElementNode {
		name := strings.ToLower(node.Data)
		if skipSubtree[name] {
			return
		}
		switch name {
		case "h1", "h2", "h3":
			if value := collapseWhitespace(nodeText(node)); value != "" {
				level := int(name[1] - '0')
				*outline = append(*outline, Heading{Level: level, Text: clip(value, maxContext*2)})
				text.WriteString("\n\n" + strings.Repeat("#", level) + " " + value + "\n\n")
			}
			// The heading is rendered whole; walking it again would duplicate it.
			return
		case "a":
			label := collapseWhitespace(nodeText(node))
			resolved, ok := resolveLink(base, attr(node, "href"))
			*links = append(*links, Link{
				Ref:     "l" + strconv.Itoa(len(*links)+1),
				Text:    clip(label, maxContext),
				URL:     resolved,
				Context: "",
			})
			if label == "" {
				return
			}
			if ok && resolved != "" {
				text.WriteString("[" + label + "](" + resolved + ")")
			} else {
				text.WriteString(label)
			}
			return
		case "table":
			table, ok := readTable(node, len(*tables)+1)
			if ok {
				*tables = append(*tables, table)
				text.WriteString("\n\n" + markdownTable(table) + "\n\n")
			}
			// The table is rendered from its cells; walking it again would repeat them.
			return
		case "li":
			text.WriteString("\n- ")
		case "br":
			text.WriteString("\n")
		}
		if blockTags[name] {
			text.WriteString("\n\n")
		}
	}
	if node.Type == xhtml.TextNode {
		text.WriteString(node.Data)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child, base, text, outline, links, tables)
	}
	if node.Type == xhtml.ElementNode && blockTags[strings.ToLower(node.Data)] {
		text.WriteString("\n\n")
	}
}

// markdownTable renders a table as a GFM pipe table, which is what a model reads
// as rows and what a spreadsheet export can be rebuilt from.
func markdownTable(table Table) string {
	if len(table.Rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range table.Rows {
		width = max(width, len(row))
	}
	if width == 0 {
		return ""
	}
	var out strings.Builder
	if table.Caption != "" {
		out.WriteString(table.Caption + "\n\n")
	}
	writeRow := func(row []string) {
		out.WriteByte('|')
		for i := 0; i < width; i++ {
			cell := ""
			if i < len(row) {
				cell = strings.ReplaceAll(row[i], "|", `\|`)
			}
			out.WriteString(" " + cell + " |")
		}
		out.WriteByte('\n')
	}
	writeRow(table.Rows[0])
	out.WriteByte('|')
	for i := 0; i < width; i++ {
		out.WriteString(" --- |")
	}
	out.WriteByte('\n')
	for _, row := range table.Rows[1:] {
		writeRow(row)
	}
	return strings.TrimRight(out.String(), "\n")
}

// resolveLink turns an href into an absolute address, dropping anything that is
// not an ordinary HTTPS page: a mailto or javascript target is not somewhere a
// reader can go, and a fragment points inside the page already held.
func resolveLink(base, href string) (string, bool) {
	reference, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	reference.Fragment = ""
	if reference.Scheme == "mailto" || reference.Scheme == "javascript" || reference.Scheme == "tel" {
		return "", false
	}
	absolute, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	resolved := absolute.ResolveReference(reference)
	if resolved.Scheme != "https" {
		return "", false
	}
	return resolved.String(), true
}

func readTable(node *xhtml.Node, index int) (Table, bool) {
	table := Table{Ref: "t" + strconv.Itoa(index)}
	var rows [][]string
	var visit func(*xhtml.Node, bool)
	visit = func(n *xhtml.Node, inRow bool) {
		if n.Type == xhtml.ElementNode {
			switch strings.ToLower(n.Data) {
			case "caption":
				table.Caption = clip(collapseWhitespace(nodeText(n)), maxContext)
				return
			case "tr":
				rows = append(rows, nil)
				inRow = true
			case "td", "th":
				if inRow && len(rows) > 0 {
					cell := collapseWhitespace(nodeText(n))
					row := &rows[len(rows)-1]
					if len(*row) < maxTableCols {
						*row = append(*row, clip(cell, maxContext))
					} else {
						table.Truncated = true
					}
					return
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child, inRow)
		}
	}
	visit(node, false)
	kept := make([][]string, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		if len(kept) >= maxTableRows {
			table.Truncated = true
			break
		}
		kept = append(kept, row)
	}
	if len(kept) == 0 {
		return Table{}, false
	}
	table.Rows = kept
	return table, true
}

// classifyPage names what this page looks like so a caller can pick a strategy
// without a domain list. It is a hint from structure, not a promise.
func classifyPage(page Page) string {
	if page.TextBytes == 0 {
		return KindEmpty
	}
	if len(page.Tables) > 0 && page.TableCount*3 >= page.LinkCount {
		return KindTable
	}
	// Many links and little prose is a directory, not an article.
	if page.LinkCount >= 12 && page.TextBytes/page.LinkCount < 160 {
		return KindIndex
	}
	return KindArticle
}

func boundedLinks(links []Link) []Link {
	if len(links) > maxLinks {
		return links[:maxLinks]
	}
	return links
}

func boundedHeadings(headings []Heading) []Heading {
	if len(headings) > maxHeadings {
		return headings[:maxHeadings]
	}
	return headings
}

func boundedTables(tables []Table) []Table {
	if len(tables) > maxTables {
		return tables[:maxTables]
	}
	return tables
}

// clip bounds one field without splitting a rune.
func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return bounded(value, limit)
}
