//go:build !unix

package state

import (
	"errors"
	"os"
)

// Lock is unsupported off unix: todobem is released for macOS and Linux only.
func Lock(path string, exclusive bool) (*os.File, error) {
	return nil, errors.New("file locks are supported on macOS and Linux only")
}
