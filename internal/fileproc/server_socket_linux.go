//go:build linux

package fileproc

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"time"
)

const socketLockMode = 0600

// socketLock serializes startup and holds ownership until the listener stops.
// A persistent lock file is safe: the kernel releases flock after process death.
func socketLock(ctx context.Context, path string) (func(), error) {
	fd, err := syscall.Open(
		path+".lock",
		syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC,
		socketLockMode,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path+".lock")
	fail := func(err error) (func(), error) { _ = file.Close(); return nil, err }
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("socket lock must be regular file"))
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(errors.New("file worker socket already owned"))
	}
	if err = removeStaleSocket(ctx, path); err != nil {
		return fail(err)
	}
	return func() { _ = file.Close() }, nil
}
func removeStaleSocket(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("file worker socket path is not a socket")
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(probeCtx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return errors.New("file worker socket is active")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return errors.New("file worker socket changed during startup")
	}
	return os.Remove(path)
}
