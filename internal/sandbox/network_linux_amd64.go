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
	bpfJumpSet        = 0x45
	bpfReturn         = 0x06
	x32SyscallBit     = 0x40000000

	sysProcessVMReadv  = 310
	sysProcessVMWritev = 311
	sysPIDFDSendSignal = 424
	sysIOUringSetup    = 425
	sysPIDFDOpen       = 434
)

func denySockets() error {
	// seccomp_data begins with nr (offset 0) and arch (offset 4). The filter
	// denies creating sockets and io_uring, along with operations that can
	// control other processes of the same service user. The child inherits no
	// host socket. Reject the x32 syscall number space on the same audit arch.
	filter := []syscall.SockFilter{
		{Code: bpfLoadWordAbs, K: 4},
		{Code: bpfJumpEqual, Jt: 1, K: auditArchX8664},
		{Code: bpfReturn, K: seccompRetKill},
		{Code: bpfLoadWordAbs, K: 0},
		{Code: bpfJumpSet, Jf: 1, K: x32SyscallBit},
		{Code: bpfReturn, K: seccompRetKill},
	}
	blocked := []uint32{
		syscall.SYS_SOCKET, syscall.SYS_SOCKETPAIR, sysIOUringSetup,
		syscall.SYS_KILL, syscall.SYS_TKILL, syscall.SYS_TGKILL,
		syscall.SYS_RT_SIGQUEUEINFO, syscall.SYS_RT_TGSIGQUEUEINFO, sysPIDFDSendSignal, sysPIDFDOpen,
		syscall.SYS_PTRACE, sysProcessVMReadv, sysProcessVMWritev,
		syscall.SYS_SETSID, syscall.SYS_SETPGID,
	}
	for _, number := range blocked {
		filter = append(filter, syscall.SockFilter{Code: bpfJumpEqual, Jf: 1, K: number}, syscall.SockFilter{Code: bpfReturn, K: seccompRetErrno | uint32(syscall.EPERM)})
	}
	filter = append(filter, syscall.SockFilter{Code: bpfReturn, K: seccompRetAllow})
	program := syscall.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_SECCOMP, seccompModeFilter, uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("seccomp socket filter: %w", errno)
	}
	return nil
}
