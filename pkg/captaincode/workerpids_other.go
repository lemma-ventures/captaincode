//go:build !darwin && !linux

package captaincode

// processStarted names a live process's start time, so a reused pid is not
// taken for the process that was recorded. "" when the process is gone.
func processStarted(pid int) string { return psStarted(pid) }
