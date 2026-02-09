//go:build windows

package autostart

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName  = "asktg"
)

func Enabled() (bool, error) {
	execPath, err := os.Executable()
	if err != nil {
		return false, err
	}
	expected := quote(execPath)
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	defer key.Close()

	value, _, err := key.GetStringValue(valueName)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(expected)), nil
}

func SetEnabled(enable bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if !enable {
		err := key.DeleteValue(valueName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(valueName, quote(execPath))
}

func quote(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return ""
	}
	if strings.HasPrefix(clean, `"`) && strings.HasSuffix(clean, `"`) {
		return clean
	}
	return `"` + clean + `"`
}
