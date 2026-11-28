package courseware

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// This reads the text layer of a PDF without a third-party dependency. It covers
// what courseware usually is - Flate-compressed content streams drawn with the
// standard text operators - and says plainly how much it missed. A silent partial
// extraction would be worse than a refusal: a student would be taught from text
// that quietly lost half the page.
//
// What it cannot read is reported rather than ignored: text drawn through a font
// encoding (hex strings needing a CMap) is counted and named in the warnings. The
// caller decides what to tell the user; this reader never claims completeness it
// cannot prove.
const (
	pdfStreamLimit = 8 << 20
	// A document's decompressed content is bounded as a whole too. A small file can
	// expand into gigabytes, and stopping at the budget is better than filling
	// memory with a lecture that nobody asked to read in full.
	pdfTotalLimit = 32 << 20
)

func extractPDF(raw []byte) (string, Extraction, error) {
	if len(raw) < 8 || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return "", Extraction{}, errors.New("不是有效的 PDF 文件")
	}
	read := &pdfRead{}
	read.scan(raw)
	text := strings.Join(strings.Fields(read.text.String()), " ")
	if text == "" {
		// Nothing at all was readable. This is the case where a scale-based refusal
		// is right: there is no partial lecture to qualify.
		if read.encodings > 0 {
			return "", Extraction{}, errors.New("这个 PDF 的文本全部使用字体编码（CMap/hex），当前提取器无法读取；可以打开原件查看")
		}
		return "", Extraction{}, errors.New("这个 PDF 没有可提取的文本层；它可能是扫描件，或使用了需要 CMap 的字体编码")
	}
	report := Extraction{Status: extractionComplete, Pages: pdfPageCount(raw)}
	if read.encodings > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("有 %d 处文本使用字体编码（CMap/hex），未能提取；这些内容不在课件文本里", read.encodings))
	}
	if read.truncated {
		report.Warnings = append(report.Warnings, "PDF 内容超过了单个文档的解压预算，只读取了前面一部分，后面的页面可能缺失")
	}
	if len(report.Warnings) > 0 {
		report.Status = extractionLimited
	}
	return text, report, nil
}

// pdfRead accumulates what one pass over the file found.
type pdfRead struct {
	text strings.Builder
	// encodings counts strings drawn through a font encoding this reader cannot
	// follow. They are real page content, so their presence is what makes the
	// result partial rather than merely short.
	encodings int
	// truncated is set when the decompression budget, not the file, ended the read.
	truncated bool
}

// scan walks for stream markers. The cross-reference table is not consulted:
// finding the markers yields the same bytes and cannot be fooled by a damaged
// xref, which exported slides very often have.
func (r *pdfRead) scan(raw []byte) {
	budget := pdfTotalLimit
	rest := raw
	for len(rest) > 0 {
		start := bytes.Index(rest, []byte("stream"))
		if start < 0 {
			return
		}
		body := rest[start+len("stream"):]
		body = bytes.TrimPrefix(body, []byte("\r\n"))
		body = bytes.TrimPrefix(body, []byte("\n"))
		end := bytes.Index(body, []byte("endstream"))
		if end < 0 {
			// A stream without its terminator is a damaged file, not a finished read.
			r.truncated = true
			return
		}
		chunk := body[:end]
		rest = body[end+len("endstream"):]
		plain, overflow, ok := decodePDFStream(chunk, budget)
		if overflow {
			r.truncated = true
		}
		if !ok {
			continue
		}
		budget -= len(plain)
		r.appendText(plain)
	}
}

// decodePDFStream returns at most limit bytes and reports whether the stream held
// more than that. Reading one byte past the limit is what distinguishes "exactly
// this long" from "cut short".
func decodePDFStream(chunk []byte, budget int) (body []byte, overflow, ok bool) {
	limit := min(pdfStreamLimit, budget)
	if limit <= 0 {
		return nil, true, false
	}
	if decoder, err := zlib.NewReader(bytes.NewReader(chunk)); err == nil {
		plain, readErr := io.ReadAll(io.LimitReader(decoder, int64(limit)+1))
		_ = decoder.Close()
		if readErr != nil {
			return nil, false, false
		}
		if len(plain) > limit {
			return plain[:limit], true, true
		}
		return plain, false, true
	}
	// Not compressed - some producers leave content streams plain.
	if len(chunk) > limit {
		return chunk[:limit], true, true
	}
	return chunk, false, true
}

// appendText pulls the operands of the text-showing operators out of one content
// stream. Literal strings are read; hex strings carry glyph codes that need the
// font's CMap to mean anything, so they are counted rather than guessed at.
func (r *pdfRead) appendText(content []byte) {
	// A TJ array draws its pieces with numbers between them, so "is TJ the next
	// token" is not how a string inside one is recognised.
	arrays := textArrays(content)
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '(':
			literal, next, ok := readPDFLiteral(content, i)
			if !ok {
				continue
			}
			if showsText(content[next:]) || inSpans(arrays, i) {
				r.text.WriteString(literal)
				r.text.WriteByte(' ')
			}
			i = next - 1
		case '<':
			// "<<" opens a dictionary, not a string.
			if i+1 < len(content) && content[i+1] == '<' {
				continue
			}
			end := bytes.IndexByte(content[i+1:], '>')
			if end < 0 {
				continue
			}
			end += i + 1
			if showsText(content[end+1:]) || inSpans(arrays, i) {
				r.encodings++
			}
			i = end
		}
	}
}

// textArrays returns the byte spans covered by TJ arrays. Literal strings are
// skipped while scanning so a bracket inside text cannot open a fake array.
func textArrays(content []byte) [][2]int {
	var spans [][2]int
	open := -1
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case '(':
			if _, after, ok := readPDFLiteral(content, i); ok {
				i = after - 1
			}
		case '[':
			if open < 0 {
				open = i
			}
		case ']':
			if open >= 0 && showsText(content[i+1:]) {
				spans = append(spans, [2]int{open, i})
			}
			open = -1
		}
	}
	return spans
}

func inSpans(spans [][2]int, at int) bool {
	for _, span := range spans {
		if at >= span[0] && at <= span[1] {
			return true
		}
	}
	return false
}

// readPDFLiteral reads a balanced (...) string, honouring backslash escapes.
func readPDFLiteral(content []byte, start int) (string, int, bool) {
	var b strings.Builder
	depth := 1
	for i := start + 1; i < len(content); i++ {
		switch content[i] {
		case '\\':
			if i+1 >= len(content) {
				return "", start, false
			}
			i++
			switch content[i] {
			case 'n', 'r', 't', 'b', 'f':
				b.WriteByte(' ')
			case '(', ')', '\\':
				b.WriteByte(content[i])
			default:
				// Octal escape: up to three digits.
				j := i
				for j < len(content) && j < i+3 && content[j] >= '0' && content[j] <= '7' {
					j++
				}
				if j > i {
					if value, err := strconv.ParseUint(string(content[i:j]), 8, 16); err == nil {
						b.WriteByte(byte(value))
					}
					i = j - 1
				}
			}
		case '(':
			depth++
			b.WriteByte(content[i])
		case ')':
			depth--
			if depth == 0 {
				return b.String(), i + 1, true
			}
			b.WriteByte(content[i])
		default:
			b.WriteByte(content[i])
		}
	}
	return "", start, false
}

// showsText reports whether the next token after a string is one that draws it.
// Text in a PDF that is never shown is a title or metadata, not page content.
func showsText(after []byte) bool {
	limit := 24
	if len(after) < limit {
		limit = len(after)
	}
	tail := strings.TrimLeft(string(after[:limit]), " \t\r\n")
	return strings.HasPrefix(tail, "Tj") || strings.HasPrefix(tail, "TJ") || strings.HasPrefix(tail, "'") || strings.HasPrefix(tail, "\"")
}

// pdfPageCount reads the page count the file declares. It is not the order this
// reader visited streams in, and pretending otherwise would put page numbers on
// text that was never assigned to a page.
func pdfPageCount(raw []byte) int {
	count := 0
	rest := raw
	for {
		at := bytes.Index(rest, []byte("/Type"))
		if at < 0 {
			return count
		}
		rest = rest[at+len("/Type"):]
		value := bytes.TrimLeft(rest, " \t\r\n")
		if bytes.HasPrefix(value, []byte("/Page")) && !bytes.HasPrefix(value, []byte("/Pages")) {
			count++
		}
	}
}
