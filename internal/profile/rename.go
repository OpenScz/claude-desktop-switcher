package profile

import (
	"os"
	"time"
)

var (
	rename              = os.Rename
	renameRetryTimeout  = 5 * time.Second
	renameRetryInterval = 100 * time.Millisecond
)

// renamePath retries while Windows refuses a rename only because another
// process briefly holds a file open below the path, such as a concurrent read
// of the profile or a virus scan.
func renamePath(from, to string) error {
	deadline := time.Now().Add(renameRetryTimeout)
	for {
		err := rename(from, to)
		if err == nil || !transientRenameError(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(renameRetryInterval)
	}
}
