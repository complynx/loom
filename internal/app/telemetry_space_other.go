//go:build !linux

package app

import "errors"

func artifactSpace(_ string) (float64, float64, error) {
	return 0, 0, errors.New("artifact space monitoring requires Linux")
}
