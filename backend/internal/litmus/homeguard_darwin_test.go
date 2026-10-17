package litmus

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
)

type flaggedHomeInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (i flaggedHomeInfo) Sys() any { return &i.stat }

func TestHomeGuardDarwinDatalessFlag(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !homeFileDataless(flaggedHomeInfo{info, syscall.Stat_t{Flags: 0x40000000}}) {
		t.Fatal("SF_DATALESS not detected")
	}
	if homeFileDataless(flaggedHomeInfo{info, syscall.Stat_t{Flags: 0x2}}) {
		t.Fatal("unrelated stat flag treated as dataless")
	}
}
