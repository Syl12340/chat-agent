package localagent

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// A Chinese-locale Windows editor still writes GB18030 by default. Refusing those
// files made the local agent useless for the machine it was built for.
func TestReadDecodesLegacyChineseText(t *testing.T) {
	dir := t.TempDir()
	gbk, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("这是本地笔记。\r\n第二行。"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "笔记.txt"), gbk, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "utf8.txt"), []byte("已经是 UTF-8。"), 0600); err != nil {
		t.Fatal(err)
	}
	guard, err := OpenGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	text, err := guard.Read("笔记.txt")
	if err != nil {
		t.Fatalf("GB18030 file refused: %v", err)
	}
	if text != "这是本地笔记。\r\n第二行。" {
		t.Fatalf("decoded = %q", text)
	}
	// A file that is already UTF-8 must not be run through the decoder.
	if text, err = guard.Read("utf8.txt"); err != nil || text != "已经是 UTF-8。" {
		t.Fatalf("utf8 = %q, %v", text, err)
	}
}

func TestReadStillRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	guard, err := OpenGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, err := guard.Read("blob.bin"); err == nil {
		t.Fatal("binary file was decoded as text")
	}
}
