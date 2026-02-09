//go:build windows

package tray

import (
	"errors"
	"sync"
	"time"

	"github.com/getlantern/systray"
)

type Manager struct {
	icon []byte

	onShow func()
	onHide func()
	onExit func()

	mu         sync.RWMutex
	started    bool
	ready      bool
	visible    bool
	showHide   *systray.MenuItem
	exitItem   *systray.MenuItem
	clickWG    sync.WaitGroup
	shutdownMu sync.Mutex
}

func New(icon []byte, onShow func(), onHide func(), onExit func()) *Manager {
	return &Manager{
		icon:    icon,
		onShow:  onShow,
		onHide:  onHide,
		onExit:  onExit,
		visible: true,
	}
}

func (m *Manager) Start() error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.mu.Unlock()

	readyCh := make(chan struct{})
	go systray.Run(func() {
		if len(m.icon) > 0 {
			systray.SetIcon(m.icon)
		}
		systray.SetTitle("asktg")
		systray.SetTooltip("asktg")
		systray.SetTrayIconClickHandler(func(button systray.TrayIconMouseButton) {
			if button != systray.TrayIconMouseButtonLeft {
				return
			}
			if m.WindowVisible() {
				return
			}
			if m.onShow != nil {
				m.onShow()
			}
			m.SetWindowVisible(true)
		})

		m.showHide = systray.AddMenuItem("Hide Window", "Hide app window")
		m.exitItem = systray.AddMenuItem("Exit", "Exit asktg")

		m.mu.Lock()
		m.ready = true
		visible := m.visible
		m.mu.Unlock()
		m.applyVisibleTitle(visible)

		m.clickWG.Add(1)
		go func() {
			defer m.clickWG.Done()
			for {
				select {
				case <-m.showHide.ClickedCh:
					if m.WindowVisible() {
						if m.onHide != nil {
							m.onHide()
						}
						m.SetWindowVisible(false)
					} else {
						if m.onShow != nil {
							m.onShow()
						}
						m.SetWindowVisible(true)
					}
				case <-m.exitItem.ClickedCh:
					if m.onExit != nil {
						m.onExit()
					}
					return
				}
			}
		}()

		close(readyCh)
	}, func() {})

	select {
	case <-readyCh:
		return nil
	case <-time.After(8 * time.Second):
		return errors.New("tray start timeout")
	}
}

func (m *Manager) Stop() {
	m.shutdownMu.Lock()
	defer m.shutdownMu.Unlock()
	if !m.started {
		return
	}
	systray.SetTrayIconClickHandler(nil)
	systray.Quit()
	m.clickWG.Wait()
	m.mu.Lock()
	m.ready = false
	m.started = false
	m.showHide = nil
	m.exitItem = nil
	m.mu.Unlock()
}

func (m *Manager) SetWindowVisible(visible bool) {
	m.mu.Lock()
	m.visible = visible
	ready := m.ready
	m.mu.Unlock()
	if ready {
		m.applyVisibleTitle(visible)
	}
}

func (m *Manager) WindowVisible() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.visible
}

func (m *Manager) Available() bool {
	return true
}

func (m *Manager) Reason() string {
	return ""
}

func (m *Manager) applyVisibleTitle(visible bool) {
	if m.showHide == nil {
		return
	}
	if visible {
		m.showHide.SetTitle("Hide Window")
		m.showHide.SetTooltip("Hide app window")
		return
	}
	m.showHide.SetTitle("Show Window")
	m.showHide.SetTooltip("Show app window")
}
