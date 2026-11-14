package server

import (
	"strings"
	"testing"
)

func TestShortReportIsLeftAlone(t *testing.T) {
	const short = `102 个版本，保留 10 个，可清理 92 个，约 946.4 MiB。这是预演，加 --apply 才会真的删除`
	if got := boundReport("  " + short + "\n"); got != short {
		t.Fatalf("short report changed: %q", got)
	}
}

func TestLongReportKeepsItsHeadAndSaysSo(t *testing.T) {
	// The real shape: a human sentence, then a pretty-printed listing.
	body := "102 个版本，保留 10 个（含当前版本）。这是预演。\n{\n  \"applied\": false,\n  \"releases\": [" +
		strings.Repeat(`{"name":"20261114-approval-record-01","size_mib":10.2},`, 800)
	got := boundReport(body)
	if len(got) > actionReportBytes+200 {
		t.Fatalf("report not bounded: %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "102 个版本，保留 10 个") {
		t.Fatal("the summary the helper writes first was cut away")
	}
	if !strings.Contains(got, "输出已截断") || !strings.Contains(got, "原文共") {
		t.Fatal("a truncated report did not say it was truncated")
	}
	// The numbers must let the reader judge what is missing.
	if !strings.Contains(got, "4096") {
		t.Fatalf("truncation note lost the bound: %q", got[len(got)-120:])
	}
}

func TestCutLandsOnAUTF8Boundary(t *testing.T) {
	// Three-byte runes straddling the bound must not leave half a character.
	body := strings.Repeat("鱼", actionReportBytes) // 3 bytes each, far past the bound
	got := boundReport(body)
	if !strings.HasPrefix(got, "鱼") {
		t.Fatal("head lost")
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("the cut produced a replacement character")
	}
}
