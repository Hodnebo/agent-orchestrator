//go:build !darwin && !linux && !windows

package triage

import (
	"context"
	"errors"
)

func readProcessIdentity(context.Context, int) (processIdentity, error) {
	return processIdentity{}, errors.New("native process identity is unavailable on this platform")
}

func readMachine(context.Context) (map[string]any, error) {
	return unknownMachine(), errors.New("native memory and load readings are unavailable on this platform")
}
