// Package autostart turns "start Uley with the computer" on and off. It is
// only ever changed by an explicit user action.
package autostart

import "os"

func exe() (string, error) { return os.Executable() }

// Set enables or disables autostart for the current user.
func Set(on bool) error { return set(on) }
