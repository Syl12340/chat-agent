//go:build windows

package localagent

import (
	"os"
	"path"
	"path/filepath"
	"syscall"
	"unsafe"
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceRoot(root *os.Root, base, from, to string) error {
	// Resolve the destination parent through os.Root before handing the already
	// guarded absolute names to the Windows atomic replace primitive.
	parent, err := root.OpenRoot(path.Dir(to))
	if err != nil {
		return err
	}
	_ = parent.Close()
	fromPtr, err := syscall.UTF16PtrFromString(filepath.Join(base, filepath.FromSlash(from)))
	if err != nil {
		return err
	}
	toPtr, err := syscall.UTF16PtrFromString(filepath.Join(base, filepath.FromSlash(to)))
	if err != nil {
		return err
	}
	const moveFileReplaceExisting = 0x1
	const moveFileWriteThrough = 0x8
	ok, _, callErr := moveFileExW.Call(uintptr(unsafe.Pointer(fromPtr)), uintptr(unsafe.Pointer(toPtr)), moveFileReplaceExisting|moveFileWriteThrough)
	if ok == 0 {
		return callErr
	}
	return nil
}
