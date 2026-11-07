//go:build windows

package sessions

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProcessRunningComparesTheRecordedStartTime(t *testing.T) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	started := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)

	if !processRunning(os.Getpid(), strconv.FormatUint(started, 10)) {
		t.Fatal("the running process with its own start time was not recognized")
	}
	if processRunning(os.Getpid(), strconv.FormatUint(started+1, 10)) {
		t.Fatal("a reused process id with another start time counted as running")
	}
	if !processRunning(os.Getpid(), "") {
		t.Fatal("a running process without a recorded start time was not recognized")
	}
}
