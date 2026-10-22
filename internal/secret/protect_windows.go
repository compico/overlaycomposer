//go:build windows

package secret

import (
	"errors"
	"syscall"
	"unsafe"
)

// Файл ключа защищён DPAPI (CryptProtectData, область — текущий пользователь Windows): скопированный на другой компьютер
// или открытый другим пользователем, он бесполезен.
const canProtect = true

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

const cryptProtectUIForbidden = 0x1

func protect(b []byte) ([]byte, error)   { return dpapi(procProtect, b) }
func unprotect(b []byte) ([]byte, error) { return dpapi(procUnprotect, b) }

func dpapi(proc *syscall.LazyProc, b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty data")
	}
	in := dataBlob{uint32(len(b)), &b[0]}
	var out dataBlob
	r, _, err := proc.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return append([]byte(nil), unsafe.Slice(out.pbData, out.cbData)...), nil
}
