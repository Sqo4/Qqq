// Package sysmon answers three questions the client asks every few
// seconds: is anyone using the computer, is a fullscreen game running, and
// is a laptop on battery. Nothing of this leaves the computer.
package sysmon

import "time"

// Monitor is an interface so tests can drive the agent with fake answers.
type Monitor interface {
	// IdleTime is the time since the last keyboard or mouse input.
	// ok is false when the OS does not tell us.
	IdleTime() (d time.Duration, ok bool)
	Fullscreen() bool
	OnBattery() bool
}

type System struct{}

func (System) IdleTime() (time.Duration, bool) { return idleTime() }
func (System) Fullscreen() bool                { return fullscreen() }
func (System) OnBattery() bool                 { return onBattery() }
