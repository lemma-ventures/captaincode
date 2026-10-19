package captaincode

import (
	"os"
	"strconv"
	"strings"
)

// processStarted names a live process's start time, so a reused pid is not
// taken for the process that was recorded. "" when the process is gone.
// Field 22 of /proc/<pid>/stat is the start time in clock ticks since boot;
// the command name before it may hold spaces, so fields count from its ")".
func processStarted(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
