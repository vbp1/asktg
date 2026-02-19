//go:build windows

package security

import (
	"encoding/base64"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const dpapiPrefix = "dpapi:"

func ProtectString(value string) (string, error) {
	data := []byte(value)
	if len(data) == 0 {
		return "", nil
	}
	var in windows.DataBlob
	in.Size = uint32(len(data))
	in.Data = &data[0]

	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	encrypted := unsafe.Slice(out.Data, out.Size)
	return dpapiPrefix + base64.StdEncoding.EncodeToString(encrypted), nil
}

func UnprotectString(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) < len(dpapiPrefix) || value[:len(dpapiPrefix)] != dpapiPrefix {
		return "", errors.New("value is not dpapi protected")
	}

	encoded := value[len(dpapiPrefix):]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}

	var in windows.DataBlob
	in.Size = uint32(len(data))
	in.Data = &data[0]

	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	plain := unsafe.Slice(out.Data, out.Size)
	return string(plain), nil
}

func IsProtectedSecret(value string) bool {
	return len(value) >= len(dpapiPrefix) && value[:len(dpapiPrefix)] == dpapiPrefix
}
