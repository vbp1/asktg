//go:build !windows

package tray

import "errors"

var errTrayUnsupported = errors.New("system tray manager is implemented for Windows in this build")

type Manager struct{}

func New(_ []byte, _ func(), _ func(), _ func()) *Manager {
	return &Manager{}
}

func (m *Manager) Start() error {
	return errTrayUnsupported
}

func (m *Manager) Stop() {}

func (m *Manager) SetWindowVisible(_ bool) {}

func (m *Manager) WindowVisible() bool {
	return true
}

func (m *Manager) Available() bool {
	return false
}

func (m *Manager) Reason() string {
	return errTrayUnsupported.Error()
}
