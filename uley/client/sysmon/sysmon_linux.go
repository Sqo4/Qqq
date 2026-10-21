package sysmon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func cmdOut(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

var uintRe = regexp.MustCompile(`\d+`)

func idleTime() (time.Duration, bool) {
	if out, err := cmdOut("xprintidle"); err == nil {
		if ms, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			return time.Duration(ms) * time.Millisecond, true
		}
	}
	// GNOME on Wayland.
	if out, err := cmdOut("gdbus", "call", "--session", "--dest", "org.gnome.Mutter.IdleMonitor",
		"--object-path", "/org/gnome/Mutter/IdleMonitor/Core", "--method", "org.gnome.Mutter.IdleMonitor.GetIdletime"); err == nil {
		if m := uintRe.FindString(strings.TrimPrefix(strings.TrimSpace(out), "(uint64")); m != "" {
			ms, _ := strconv.ParseInt(m, 10, 64)
			return time.Duration(ms) * time.Millisecond, true
		}
	}
	return 0, false
}

func fullscreen() bool { return false }

func onBattery() bool {
	supplies, _ := filepath.Glob("/sys/class/power_supply/*")
	hasMains := false
	for _, p := range supplies {
		typ, _ := os.ReadFile(filepath.Join(p, "type"))
		if strings.TrimSpace(string(typ)) != "Mains" {
			continue
		}
		hasMains = true
		online, _ := os.ReadFile(filepath.Join(p, "online"))
		if strings.TrimSpace(string(online)) == "1" {
			return false
		}
	}
	return hasMains
}
