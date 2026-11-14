//go:build linux

package llamacpp

import "syscall"

// Se o nexus morrer (até com kill -9), o kernel encerra o llama-server junto.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM, Setpgid: true}
}
