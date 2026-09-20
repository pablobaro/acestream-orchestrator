//go:build windows

package vpn

import (
	"errors"
	"syscall"
)

// Winsock error codes. syscall.Errno.Is on Windows does not map the POSIX
// names (syscall.ECONNREFUSED and friends) onto their WSA equivalents, so the
// dial errors the runtime actually returns here carry these values instead.
const (
	wsaeConnRefused = syscall.Errno(10061) // WSAECONNREFUSED
	wsaeNetUnreach  = syscall.Errno(10051) // WSAENETUNREACH
	wsaeHostUnreach = syscall.Errno(10065) // WSAEHOSTUNREACH
)

func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaeConnRefused)
}

func isNetUnreachable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, wsaeNetUnreach) ||
		errors.Is(err, wsaeHostUnreach)
}
