//go:build net && windows

package repomofo

import "os/exec"

func setProcGroup(cmd *exec.Cmd) {
	// no-op on windows
}

func killProcGroup(cmd *exec.Cmd) {
	cmd.Process.Kill()
}
