//go:build !linux

package llamacpp

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
