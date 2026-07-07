//go:build !windows

package main

import "syscall"

func diskFree(path string) (int64, error) {
	free, _, err := diskUsage(path)
	return free, err
}

// diskUsage returns (available-to-caller, total) capacity in bytes for the
// filesystem backing path — used by the finalize buffer-percent precondition.
func diskUsage(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}
