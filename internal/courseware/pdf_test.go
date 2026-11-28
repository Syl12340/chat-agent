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
	return buildPDFPages(t, "1", content)
}

// buildPDFPages lets a test declare how many pages the file claims, because the
// page count is read from the file and must not be inferred from stream order.
func buildPDFPages(t *testing.T, pageCount, content string) []byte {
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
	for i := 0; i < mustAtoi(t, pageCount); i++ {
		out.WriteString("/Type /Page\n")
	}
	out.WriteString("1 0 obj\n<< /Length " + strconv.Itoa(compressed.Len()) + " /Filter /FlateDecode >>\nstream\n")
	out.Write(compressed.Bytes())
	out.WriteString("\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return out.Bytes()
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestExtractPDFReadsACompressedTextLayer(t *testing.T) {
	raw := buildPDF(t, "BT /F1 12 Tf 72 720 Td (Limits come before derivatives.) Tj ET")
	text, report, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("a plain text layer should extract: %v", err)
	}
	if !strings.Contains(text, "Limits come before derivatives.") {
		t.Fatalf("extracted text = %q, want the page sentence", text)
	}
	if report.Status != extractionComplete || len(report.Warnings) != 0 {
		t.Fatalf("nothing was skipped, so this is complete: %+v", report)
	}
}

// Text that appears in the file but is never drawn is not page content.
func TestExtractPDFIgnoresUndrawnStrings(t *testing.T) {
	raw := buildPDF(t, "/Title (Internal heading not on the page)\nBT /F1 12 Tf (Real body text) Tj ET")
	text, _, err := extractPDF(raw)
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
	if _, _, err := extractPDF(raw); err == nil {
		t.Fatal("an image-only page must be reported, not returned as empty text")
	}
	if _, _, err := extractPDF([]byte("not a pdf at all")); err == nil {
		t.Fatal("a non-PDF must be rejected")
	}
}

func TestExtractPDFHandlesEscapedParentheses(t *testing.T) {
	raw := buildPDF(t, `BT /F1 12 Tf (Set \{a\} and \(b\) are open) Tj ET`)
	text, _, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	if !strings.Contains(text, "open") {
		t.Fatalf("escaping broke the literal: %q", text)
	}
}

// The heart of the reported defect: one readable page next to text drawn through a
// font encoding used to look exactly like a complete extraction.
func TestExtractPDFReportsEncodingItCannotRead(t *testing.T) {
	raw := buildPDF(t, "BT /F1 12 Tf (Readable first page) Tj ET\nBT /F1 12 Tf <002A0044> Tj ET")
	text, report, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("the readable part should still extract: %v", err)
	}
	if !strings.Contains(text, "Readable first page") {
		t.Fatalf("readable text was missed: %q", text)
	}
	if report.Status != extractionLimited {
		t.Fatalf("a page of text was skipped, so this cannot be complete: %+v", report)
	}
	if len(report.Warnings) == 0 || !strings.Contains(strings.Join(report.Warnings, " "), "字体编码") {
		t.Fatalf("the warning must name what was missed: %+v", report.Warnings)
	}
}

// A TJ array draws its pieces as separate strings; they all belong to the page.
func TestExtractPDFReadsTJArrays(t *testing.T) {
	raw := buildPDF(t, "BT /F1 12 Tf [(Limits come) -20 (before) -20 (derivatives.)] TJ ET")
	text, _, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("a TJ array is ordinary text: %v", err)
	}
	for _, want := range []string{"Limits come", "before", "derivatives."} {
		if !strings.Contains(text, want) {
			t.Fatalf("TJ piece %q missing from %q", want, text)
		}
	}
}

// Everything unreadable is a different outcome from partly readable: there is no
// partial lecture to qualify, so the upload is marked failed instead of limited.
func TestExtractPDFFailsWhenOnlyEncodedTextExists(t *testing.T) {
	raw := buildPDF(t, "BT /F1 12 Tf <002A0044> Tj ET")
	if _, _, err := extractPDF(raw); err == nil {
		t.Fatal("text that cannot be decoded must not be presented as extracted")
	} else if !strings.Contains(err.Error(), "CMap") {
		t.Fatalf("the refusal should say why: %v", err)
	}
}

// A stream that reads to the budget was cut short by this reader, not by the file.
// Only the byte limit can end the read early, so it is the limit that is tested.
func TestDecodePDFStreamReportsOverflow(t *testing.T) {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(strings.Repeat("A", 4096))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body, overflow, ok := decodePDFStream(compressed.Bytes(), 1024)
	if !ok || !overflow || len(body) != 1024 {
		t.Fatalf("a budget-limited read must be reported: len=%d overflow=%v ok=%v", len(body), overflow, ok)
	}
	if _, overflow, _ := decodePDFStream(compressed.Bytes(), 1<<20); overflow {
		t.Fatal("a stream inside the budget is not truncated")
	}
	if _, overflow, ok := decodePDFStream(compressed.Bytes(), 0); ok || !overflow {
		t.Fatal("an exhausted budget must stop the read and report it")
	}
}

// The declared page count comes from the file. It must not be taken from the order
// this reader happened to visit streams in.
func TestExtractPDFReadsDeclaredPageCount(t *testing.T) {
	raw := buildPDFPages(t, "7", "BT /F1 12 Tf (One stream, seven pages) Tj ET")
	_, report, err := extractPDF(raw)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	if report.Pages != 7 {
		t.Fatalf("page count = %d, want the 7 the file declares", report.Pages)
	}
}
