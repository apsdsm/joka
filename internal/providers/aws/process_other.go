//go:build !unix

package aws

import "os/exec"

// contain is a no-op where process groups are not available. WaitDelay still
// bounds the wait, so a surviving child costs a held port rather than a hang.
func contain(cmd *exec.Cmd) {}
