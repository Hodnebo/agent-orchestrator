package litmus

import (
	"io/fs"
	"syscall"
)

func homeFileDataless(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Flags&0x40000000 != 0 // SF_DATALESS
}
