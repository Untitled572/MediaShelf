//go:build windows

package agent

import (
	"syscall"
	"unsafe"
)

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceJournalFile(src, dst string) error {
	s, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	d, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	// MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH
	r, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(d)), 0x1|0x8)
	if r == 0 {
		return callErr
	}
	return nil
}
