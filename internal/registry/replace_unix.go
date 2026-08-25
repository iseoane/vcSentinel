//go:build !windows

package registry

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
