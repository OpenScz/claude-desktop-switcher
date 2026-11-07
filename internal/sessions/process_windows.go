//go:build windows

package sessions

import (
	"strconv"

	"golang.org/x/sys/windows"
)

const processStillActive = 259

// A process id can be reused, so the start time Claude Code recorded must
// match the running process when it is known.
func processRunning(pid int, procStart string) bool {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var code uint32
	if windows.GetExitCodeProcess(process, &code) != nil || code != processStillActive {
		return false
	}
	started, err := strconv.ParseUint(procStart, 10, 64)
	if err != nil {
		return true
	}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(process, &created, &exited, &kernel, &user) != nil {
		return true
	}
	return uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) == started
}
