//go:build windows

package store

import (
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func diskUsage(path string) (total, free, used uint64, err error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	var available uint64
	var totalBytes uint64
	var freeBytes uint64
	r, _, callErr := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&available)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&freeBytes)),
	)
	if r == 0 {
		err = callErr
		return
	}
	total, free = totalBytes, available
	used = total - free
	return
}
