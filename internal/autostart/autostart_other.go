//go:build !windows

package autostart

func Enabled() (bool, error) {
	return false, nil
}

func SetEnabled(enable bool) error {
	_ = enable
	return nil
}
