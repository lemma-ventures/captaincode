package captaincode

import (
	"fmt"
	"syscall"
	"unsafe"
)

// processStarted names a live process's start time, so a reused pid is not
// taken for the process that was recorded. "" when the process is gone.
// kinfo_proc starts with extern_proc, whose first field is p_starttime.
func processStarted(pid int) string {
	mib := [4]int32{1, 14, 1, int32(pid)} // CTL_KERN, KERN_PROC, KERN_PROC_PID
	buf := make([]byte, 1024)
	n := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if errno != 0 || n < 16 {
		return ""
	}
	sec := *(*int64)(unsafe.Pointer(&buf[0]))
	usec := *(*int32)(unsafe.Pointer(&buf[8]))
	if sec == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%06d", sec, usec)
}
