//go:build !windows

package secret

import "errors"

// Вне Windows файл ключа защищён только правами 0600.
const canProtect = false

func protect(b []byte) ([]byte, error) { return b, nil }

func unprotect([]byte) ([]byte, error) {
	return nil, errors.New("the key file was created on Windows (DPAPI) and cannot be read on this system")
}
