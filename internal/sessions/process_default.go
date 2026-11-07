//go:build !windows

package sessions

func processRunning(int, string) bool { return false }
