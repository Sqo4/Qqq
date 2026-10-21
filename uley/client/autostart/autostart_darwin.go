package autostart

import (
	"html"
	"os"
	"path/filepath"
)

func set(on bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "org.uley.client.plist")
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	p, err := exe()
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>org.uley.client</string>
<key>ProgramArguments</key><array><string>` + html.EscapeString(p) + `</string><string>--background</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
`
	return os.WriteFile(path, []byte(plist), 0o644)
}
