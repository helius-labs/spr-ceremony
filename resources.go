package ceremony

import (
	"fmt"
	"syscall"
)

func CheckDiskSpace(path string, required uint64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return err
	}
	available := uint64(st.Bavail) * uint64(st.Bsize)
	if available < required {
		return fmt.Errorf("need %d free bytes; only %d available", required, available)
	}
	return nil
}
