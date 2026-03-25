//go:build darwin

package gitapply

import "golang.org/x/sys/unix"

// buildSysProcAttr returns the Darwin SysProcAttr.
// Darwin does not support Pdeathsig so only Setpgid is set.
func buildSysProcAttr() *unix.SysProcAttr {
	return &unix.SysProcAttr{
		Setpgid: true,
	}
}
