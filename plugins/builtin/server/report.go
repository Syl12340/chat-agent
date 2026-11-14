package server

import (
	"fmt"
	"strings"
)

// actionReportBytes bounds how much of a maintenance helper's report reaches the
// model.
//
// A helper's stdout is a machine-readable report. Without this bound one dry run
// put roughly 12 KiB of release listings into a single reply: the operator asked
// what would happen and got a wall of JSON read back to them. The summary the
// helper writes first is what a conversation needs; the rest stays on the machine
// that produced it, where it can be produced again.
//
// 4 KiB fits a summary plus a few dozen records, and leaves room in the request
// for the conversation itself.
const actionReportBytes = 4 << 10

// boundReport keeps the head of a report and says plainly that it was cut, so a
// truncated listing can never be read as a complete one. A cut is placed on a
// valid UTF-8 boundary: half a rune would corrupt the surrounding JSON just as
// badly as half an object.
func boundReport(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= actionReportBytes {
		return text
	}
	head := strings.TrimRight(strings.ToValidUTF8(text[:actionReportBytes], ""), "\n")
	return fmt.Sprintf("%s\n…（输出已截断：只保留前 %d 字节，原文共 %d 字节）", head, actionReportBytes, len(text))
}
