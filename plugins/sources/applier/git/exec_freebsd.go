//go:build freebsd

package gitapply

import "golang.org/x/sys/unix"

// buildSysProcAttr returns the FreeBSD SysProcAttr.
// FreeBSD supports Pdeathsig so we set both Setpgid and Pdeathsig SIGTERM.
func buildSysProcAttr() *unix.SysProcAttr {
	return &unix.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: unix.SIGTERM,
	}
}
