package courseware

import (
	"bytes"
	"compress/zlib"
	"strconv"
	"strings"
	"testing"
)

// buildPDF writes the smallest PDF that carries a text layer, with the content
// stream Flate-compressed the way real producers write it.
func buildPDF(t *testing.T, content string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	out.WriteString("1 0 obj\n<< /Length " + strconv.Itoa(compressed.Len()) + " /Filter /FlateDecode >>\nstream\n")
	out.Write(compressed.Bytes())
	out.WriteString("\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return out.Bytes()
}

func TestExtractPDFReadsACompressedTextLayer(t *testing.T) {
	raw := buildPDF(t, "BT /F1 12 Tf 72 720 Td (Limits come before derivatives.) Tj ET")
	text, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("a plain text layer should extract: %v", err)
	}
	if !strings.Contains(text, "Limits come before derivatives.") {
		t.Fatalf("extracted text = %q, want the page sentence", text)
	}
}

// Text that appears in the file but is never drawn is not page content.
func TestExtractPDFIgnoresUndrawnStrings(t *testing.T) {
	raw := buildPDF(t, "/Title (Internal heading not on the page)\nBT /F1 12 Tf (Real body text) Tj ET")
	text, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	if strings.Contains(text, "Internal heading") {
		t.Fatalf("undrawn text leaked into the lecture: %q", text)
	}
	if !strings.Contains(text, "Real body text") {
		t.Fatalf("drawn text was missed: %q", text)
	}
}

// A scan, or a font encoding this reader cannot follow, must say so. Returning an
// empty string would look like an empty lecture.
func TestExtractPDFRefusesWhenThereIsNoTextLayer(t *testing.T) {
	raw := buildPDF(t, "q 100 0 0 100 0 0 cm /Im1 Do Q")
	if _, err := extractPDF(raw); err == nil {
		t.Fatal("an image-only page must be reported, not returned as empty text")
	}
	if _, err := extractPDF([]byte("not a pdf at all")); err == nil {
		t.Fatal("a non-PDF must be rejected")
	}
}

func TestExtractPDFHandlesEscapedParentheses(t *testing.T) {
	raw := buildPDF(t, `BT /F1 12 Tf (Set \{a\} and \(b\) are open) Tj ET`)
	text, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	if !strings.Contains(text, "open") {
		t.Fatalf("escaping broke the literal: %q", text)
	}
}
