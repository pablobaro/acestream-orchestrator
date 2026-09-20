//go:build !windows

package vpn

import (
	"errors"
	"syscall"
)

func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func isNetUnreachable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}
