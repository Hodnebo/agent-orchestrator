package litmus

import (
	"cmp"
	"strings"
)

// rawCDPParams joins split CDP JSON parameters and defaults omitted params to an empty object.
func rawCDPParams(args []string) string {
	return cmp.Or(strings.Join(args[1:], ""), "{}")
}
