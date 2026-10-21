package sysmon

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	kernel  = windows.NewLazySystemDLL("kernel32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	pGetLastInputInfo     = user32.NewProc("GetLastInputInfo")
	pGetForegroundWindow  = user32.NewProc("GetForegroundWindow")
	pGetWindowRect        = user32.NewProc("GetWindowRect")
	pMonitorFromWindow    = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	pGetShellWindow       = user32.NewProc("GetShellWindow")
	pGetDesktopWindow     = user32.NewProc("GetDesktopWindow")
	pGetClassNameW        = user32.NewProc("GetClassNameW")
	pGetTickCount         = kernel.NewProc("GetTickCount")
	pGetSystemPowerStatus = kernel.NewProc("GetSystemPowerStatus")
	pSHQueryUNState       = shell32.NewProc("SHQueryUserNotificationState")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

func idleTime() (time.Duration, bool) {
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if r, _, _ := pGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0, false
	}
	now, _, _ := pGetTickCount.Call()
	return time.Duration(uint32(now)-li.dwTime) * time.Millisecond, true
}

type rect struct{ left, top, right, bottom int32 }

type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

const (
	qunsBusy                 = 2
	qunsRunningD3DFullScreen = 3
	qunsPresentationMode     = 4
	monitorDefaultToNearest  = 2
)

func fullscreen() bool {
	var state int32
	if r, _, _ := pSHQueryUNState.Call(uintptr(unsafe.Pointer(&state))); r == 0 {
		switch state {
		case qunsBusy, qunsRunningD3DFullScreen, qunsPresentationMode:
			return true
		}
	}
	// Borderless-window games do not trigger the state above, so also
	// check whether the foreground window covers its whole monitor.
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	shell, _, _ := pGetShellWindow.Call()
	desktop, _, _ := pGetDesktopWindow.Call()
	if hwnd == shell || hwnd == desktop {
		return false
	}
	buf := make([]uint16, 64)
	pGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	switch windows.UTF16ToString(buf) {
	case "WorkerW", "Progman", "Shell_TrayWnd":
		return false
	}
	var wr rect
	if r, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); r == 0 {
		return false
	}
	mon, _, _ := pMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return false
	}
	m := mi.rcMonitor
	return wr.left <= m.left && wr.top <= m.top && wr.right >= m.right && wr.bottom >= m.bottom
}

type powerStatus struct {
	acLineStatus        byte
	batteryFlag         byte
	batteryLifePercent  byte
	systemStatusFlag    byte
	batteryLifeTime     uint32
	batteryFullLifeTime uint32
}

func onBattery() bool {
	var ps powerStatus
	if r, _, _ := pGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&ps))); r == 0 {
		return false
	}
	return ps.acLineStatus == 0
}
