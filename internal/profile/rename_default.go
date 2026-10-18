//go:build !windows

package profile

func transientRenameError(error) bool { return false }
