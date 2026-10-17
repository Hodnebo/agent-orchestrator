//go:build !darwin

package litmus

import "errors"

func cloneInstallPath(source, dest string) error {
	return errors.New("private agent installs require APFS clonefile on macOS")
}
