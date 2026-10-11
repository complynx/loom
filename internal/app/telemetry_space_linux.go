//go:build linux

package app

import "syscall"

func artifactSpace(root string) (float64, float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return 0, 0, err
	}
	return float64(stat.Bavail) * float64(stat.Bsize), float64(stat.Blocks) * float64(stat.Bsize), nil
}
