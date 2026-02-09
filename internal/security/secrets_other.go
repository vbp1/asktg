//go:build !windows

package security

func ProtectString(value string) (string, error) {
	return value, nil
}

func UnprotectString(value string) (string, error) {
	return value, nil
}

func IsProtectedSecret(value string) bool {
	return false
}
