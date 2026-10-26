package systemupdate

import "syscall"

// syscallStatfs and syscallStatfsCall wrap syscall.Statfs
// so checkDiskSpace compiles on Linux (Raspberry Pi).
type syscallStatfs = syscall.Statfs_t

func syscallStatfsCall(path string, stat *syscall.Statfs_t) error {
	return syscall.Statfs(path, stat)
}
