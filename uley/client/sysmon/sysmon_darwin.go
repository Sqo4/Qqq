package sysmon

import (
	"context"
	"os/exec"
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

var hidIdle = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

func idleTime() (time.Duration, bool) {
	out, err := cmdOut("ioreg", "-c", "IOHIDSystem", "-d", "4")
	if err != nil {
		return 0, false
	}
	m := hidIdle.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	ns, _ := strconv.ParseInt(m[1], 10, 64)
	return time.Duration(ns), true
}

func fullscreen() bool { return false }

func onBattery() bool {
	out, err := cmdOut("pmset", "-g", "batt")
	return err == nil && strings.Contains(out, "Battery Power")
}
