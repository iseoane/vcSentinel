//go:build windows

package registry

import (
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

// replaceFile uses MoveFileExW because os.Rename documents non-Unix rename as
// non-atomic. REPLACE_EXISTING preserves the target without a delete gap, and
// WRITE_THROUGH requests that the replacement reach stable storage first.
func replaceFile(source, target string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(targetPtr)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		if callErr != nil {
			return callErr
		}
		return syscall.EINVAL
	}
	return nil
}
