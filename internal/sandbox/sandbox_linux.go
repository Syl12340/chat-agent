//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"
)

// Landlock system call numbers. They are the same on every architecture Go
// supports here because Landlock was assigned a single range in the generic
// syscall table.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	createRulesetVersion = 1
	rulePathBeneath      = 1

	// Landlock access rights, named as in the kernel headers.
	accessExecute    = 1 << 0
	accessWriteFile  = 1 << 1
	accessReadFile   = 1 << 2
	accessReadDir    = 1 << 3
	accessRemoveDir  = 1 << 4
	accessRemoveFile = 1 << 5
	accessMakeChar   = 1 << 6
	accessMakeDir    = 1 << 7
	accessMakeReg    = 1 << 8
	accessMakeSock   = 1 << 9
	accessMakeFifo   = 1 << 10
	accessMakeBlock  = 1 << 11
	accessMakeSym    = 1 << 12
	// ABI 3 added truncate; ABI 2 added refer. Both are only meaningful once the
	// kernel reports them, so they are added conditionally below.
	accessRefer    = 1 << 13
	accessTruncate = 1 << 14

	// ABI 4 added network scoping.
	accessNetBindTCP    = 1 << 0
	accessNetConnectTCP = 1 << 1

	prSetNoNewPrivs = 38

	// syscall.O_PATH is not exported by the standard library. It opens a
	// reference to the path itself, without read permission, which is exactly
	// what a Landlock rule needs.
	oPath = 0x200000
)

// rightsFor returns the file access mask this kernel understands.
func rightsFor(abi int) uint64 {
	mask := uint64(accessExecute | accessWriteFile | accessReadFile | accessReadDir |
		accessRemoveDir | accessRemoveFile | accessMakeChar | accessMakeDir |
		accessMakeReg | accessMakeSock | accessMakeFifo | accessMakeBlock | accessMakeSym)
	if abi >= 2 {
		mask |= accessRefer
	}
	if abi >= 3 {
		mask |= accessTruncate
	}
	return mask
}

const (
	readRights  = accessExecute | accessReadFile | accessReadDir
	writeRights = accessWriteFile | accessRemoveDir | accessRemoveFile | accessMakeChar |
		accessMakeDir | accessMakeReg | accessMakeSock | accessMakeFifo | accessMakeBlock | accessMakeSym |
		accessRefer | accessTruncate
	// writeFileRights writes to existing files without the creation and removal
	// rights. /dev/null is the reason this exists.
	writeFileRights = readRights | accessWriteFile | accessTruncate
)

type rulesetAttr struct {
	HandledAccessFS  uint64
	HandledAccessNet uint64
}

type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int32
	_             uint32
}

func syscall3(num uintptr, a1, a2, a3 uintptr) (uintptr, error) {
	r, _, errno := syscall.Syscall(num, a1, a2, a3)
	if errno != 0 {
		return r, errno
	}
	return r, nil
}

// Probe reports whether Landlock is usable here.
func Probe() Status {
	version, err := syscall3(sysLandlockCreateRuleset, 0, 0, createRulesetVersion)
	if err != nil {
		return Status{Reason: "内核没有启用 Landlock（" + err.Error() + "）"}
	}
	abi := int(version)
	if abi < 1 {
		return Status{Reason: "Landlock 不可用"}
	}
	return Status{ABI: abi}
}

// Restrict confines the current process. It must be called before the work that
// needs confinement begins, and it cannot be undone.
func Restrict(policy Policy) error {
	status := Probe()
	if status.ABI < 1 {
		return errors.New(status.Reason)
	}
	abi := status.ABI

	// restrict_self refuses to run unless the process can no longer gain
	// privilege: an unconfined setuid binary would otherwise escape.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("设置 no_new_privs 失败：%w", errno)
	}

	attr := rulesetAttr{HandledAccessFS: rightsFor(abi)}
	size := uintptr(unsafe.Sizeof(attr.HandledAccessFS))
	if policy.DenyTCP && abi >= 4 {
		attr.HandledAccessNet = accessNetBindTCP | accessNetConnectTCP
		size = uintptr(unsafe.Sizeof(attr))
	}
	fd, err := syscall3(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), size, 0)
	if err != nil {
		return fmt.Errorf("创建 Landlock 规则集失败：%w", err)
	}
	defer syscall.Close(int(fd))

	// Every path is opened before any rule is added, so a missing directory is
	// reported as a sandbox failure instead of a partially applied policy.
	grant := func(path string, rights uint64) error {
		clean := filepath.Clean(path)
		if !filepath.IsAbs(clean) {
			return fmt.Errorf("沙箱路径必须是绝对路径：%q", path)
		}
		parent, err := syscall.Open(clean, oPath|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("打开沙箱路径 %s 失败：%w", clean, err)
		}
		defer syscall.Close(parent)
		rule := pathBeneathAttr{AllowedAccess: rights & attr.HandledAccessFS, ParentFD: int32(parent)}
		if _, err := syscall3(sysLandlockAddRule, fd, rulePathBeneath, uintptr(unsafe.Pointer(&rule))); err != nil {
			return fmt.Errorf("为 %s 添加 Landlock 规则失败：%w", clean, err)
		}
		return nil
	}

	for _, path := range policy.ReadOnly {
		if err := grant(path, readRights); err != nil {
			return err
		}
	}
	for _, path := range policy.WritableFiles {
		if err := grant(path, writeFileRights); err != nil {
			return err
		}
	}
	for _, path := range policy.Writable {
		if err := grant(path, readRights|writeRights); err != nil {
			return err
		}
	}
	if _, err := syscall3(sysLandlockRestrictSelf, fd, 0, 0); err != nil {
		return fmt.Errorf("应用 Landlock 规则失败：%w", err)
	}
	return nil
}

// Run applies the policy and replaces the current process with argv. It is the
// entry point for the internal re-exec mode.
//
// The arguments travel as their own argv slice, never through a shell, so a
// command containing spaces or quotes reaches exec unchanged.
func Run(policy Policy, argv []string) error {
	if len(argv) == 0 {
		return errors.New("沙箱需要一个要执行的命令")
	}
	if err := Restrict(policy); err != nil {
		return err
	}
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("找不到命令 %q：%w", argv[0], err)
	}
	return syscall.Exec(binary, argv, []string{"PATH=/usr/bin:/bin:/usr/local/bin", "LANG=C.UTF-8", "HOME=/nonexistent"})
}

// ParseArgs reads the internal re-exec form:
//
//	__sandbox [--ro DIR]... [--rw DIR]... [--rwfile DIR]... [--no-tcp] -- COMMAND [ARG]...
func ParseArgs(args []string) (Policy, []string, error) {
	policy := Policy{}
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "--":
			i++
			if i >= len(args) {
				return policy, nil, errors.New("沙箱缺少要执行的命令")
			}
			return policy, args[i:], nil
		case "--ro", "--rw", "--rwfile":
			if i+1 >= len(args) {
				return policy, nil, fmt.Errorf("%s 需要一个目录", args[i])
			}
			switch args[i] {
			case "--ro":
				policy.ReadOnly = append(policy.ReadOnly, args[i+1])
			case "--rwfile":
				policy.WritableFiles = append(policy.WritableFiles, args[i+1])
			default:
				policy.Writable = append(policy.Writable, args[i+1])
			}
			i++
		case "--no-tcp":
			policy.DenyTCP = true
		default:
			return policy, nil, fmt.Errorf("无法识别的沙箱参数：%q", args[i])
		}
	}
	return policy, nil, errors.New("沙箱缺少 -- 分隔符")
}

// Describe renders the status for a status endpoint or a page.
func Describe() string {
	status := Probe()
	if status.ABI < 1 {
		return "不可用：" + status.Reason
	}
	return "Landlock ABI " + strconv.Itoa(status.ABI)
}

