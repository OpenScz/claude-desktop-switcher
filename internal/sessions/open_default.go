//go:build !windows

package sessions

import "os"

func openShared(path string) (*os.File, error) {
	return os.Open(path)
}
