//go:build linux && amd64

package sandbox

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	seccompModeFilter = 2
	seccompRetKill    = 0x80000000
	seccompRetErrno   = 0x00050000
	seccompRetAllow   = 0x7fff0000
	auditArchX8664    = 0xc000003e
	bpfLoadWordAbs    = 0x20
	bpfJumpEqual      = 0x15
	bpfReturn         = 0x06
)

func denySockets() error {
	// seccomp_data begins with nr (offset 0) and arch (offset 4). The filter
	// refuses socket creation itself, covering TCP, UDP and AF_UNIX without an
	// endless protocol-specific syscall list. The child inherits no host socket.
	filter := []syscall.SockFilter{
		{Code: bpfLoadWordAbs, K: 4},
		{Code: bpfJumpEqual, Jt: 1, K: auditArchX8664},
		{Code: bpfReturn, K: seccompRetKill},
		{Code: bpfLoadWordAbs, K: 0},
		{Code: bpfJumpEqual, Jf: 1, K: syscall.SYS_SOCKET},
		{Code: bpfReturn, K: seccompRetErrno | uint32(syscall.EPERM)},
		{Code: bpfJumpEqual, Jf: 1, K: syscall.SYS_SOCKETPAIR},
		{Code: bpfReturn, K: seccompRetErrno | uint32(syscall.EPERM)},
		{Code: bpfReturn, K: seccompRetAllow},
	}
	program := syscall.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_SECCOMP, seccompModeFilter, uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("seccomp socket filter: %w", errno)
	}
	return nil
}
