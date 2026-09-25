//go:build unix

package aws

import (
	"os/exec"
	"syscall"
)

// contain puts the command in its own process group and makes cancellation
// kill the whole group.
//
// `aws ssm start-session` is a launcher: the process that actually holds the
// forwarded port is session-manager-plugin, its child. Killing only the CLI
// leaves the plugin running, holding the local port — so the next run cannot
// bind it — and holding the inherited stderr pipe, which is what made Wait
// block for ever.
//
// Killing the group is the fix for the orphan. WaitDelay in Open is the fix for
// the pipe, and is kept as well: a grandchild that has escaped the group, or a
// platform where this is a no-op, must still not be able to hang joka.
func contain(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}

		// Negative pid means the process group. The CLI is its leader because
		// of Setpgid above, so this reaches the plugin too.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// It may have already gone, which is not a failure to cancel.
			return cmd.Process.Kill()
		}

		return nil
	}
}
