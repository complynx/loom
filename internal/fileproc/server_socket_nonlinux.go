//go:build !linux

package fileproc

import (
	"context"
	"errors"
	"os"
)

func socketLock(_ context.Context, path string) (func(), error) {
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("file worker requires an unused socket path on this platform")
	}
	return func() {}, nil
}
