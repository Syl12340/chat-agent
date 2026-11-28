package courseware

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"strconv"
	"strings"
)

// This reads the text layer of a PDF without a third-party dependency. It covers
// what courseware usually is - Flate-compressed content streams drawn with the
// standard text operators - and refuses clearly on everything else. A silent
// partial extraction would be worse than a refusal: a student would be taught
// from text that quietly lost half the page.
const pdfStreamLimit = 8 << 20

func extractPDF(raw []byte) (string, error) {
	if len(raw) < 8 || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return "", errors.New("不是有效的 PDF 文件")
	}
	var out strings.Builder
	for _, content := range pdfStreams(raw) {
		appendPDFText(content, &out)
	}
	text := strings.Join(strings.Fields(out.String()), " ")
	if text == "" {
		return "", errors.New("这个 PDF 没有可提取的文本层；它可能是扫描件，或使用了需要 CMap 的字体编码")
	}
	return text, nil
}

// pdfStreams returns the decoded body of every content stream it can find. The
// cross-reference table is not consulted: walking for stream markers finds the same
// bytes and cannot be fooled by a damaged xref, which is common in exported slides.
func pdfStreams(raw []byte) [][]byte {
	out := make([][]byte, 0, 16)
	rest := raw
	for {
		start := bytes.Index(rest, []byte("stream"))
		if start < 0 {
			return out
		}
		body := rest[start+len("stream"):]
		body = bytes.TrimPrefix(body, []byte("\r\n"))
		body = bytes.TrimPrefix(body, []byte("\n"))
		end := bytes.Index(body, []byte("endstream"))
		if end < 0 {
			return out
		}
		chunk := body[:end]
		if decoder, err := zlib.NewReader(bytes.NewReader(chunk)); err == nil {
			plain, readErr := io.ReadAll(io.LimitReader(decoder, pdfStreamLimit))
			_ = decoder.Close()
			if readErr == nil {
				out = append(out, plain)
			}
		} else {
			// Not compressed - some producers leave content streams plain.
			out = append(out, chunk)
		}
		rest = body[end+len("endstream"):]
	}
}

// appendPDFText pulls the operands of the text-showing operators out of one
// content stream. Only literal strings are read; hex strings carry glyph codes
// that need the font's CMap to mean anything, so they are skipped rather than
// guessed at.
func appendPDFText(content []byte, out *strings.Builder) {
	for i := 0; i < len(content); i++ {
		if content[i] != '(' {
			continue
		}
		literal, next, ok := readPDFLiteral(content, i)
		if !ok {
			continue
		}
		i = next - 1
		if showsText(content[next:]) {
			out.WriteString(literal)
			out.WriteByte(' ')
		}
	}
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
