//go:build !windows

package replica

import "os"

func publishImmutable(source string, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return os.Remove(source)
}
