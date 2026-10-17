//go:build !darwin

package litmus

import "io/fs"

func homeFileDataless(fs.FileInfo) bool { return false }
