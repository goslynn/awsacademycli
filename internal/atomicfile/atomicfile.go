// Package atomicfile writes files atomically.
//
// The state files are rewritten on every operation; a writer interrupted
// halfway through would leave truncated credentials or cookies, and the next
// run would fail to parse them. Writing to a temporary file in the same
// directory and renaming avoids that intermediate state.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write dumps data into path with the given permissions, with no visible
// intermediate states.
func Write(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
