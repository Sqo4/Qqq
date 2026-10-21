package autostart

import (
	"os"
	"path/filepath"
)

func set(on bool) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "autostart", "uley.desktop")
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
	entry := "[Desktop Entry]\nType=Application\nName=Uley\nComment=Улей: распределённая нейросеть\nExec=\"" + p + "\" --background\nIcon=uley\nX-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(path, []byte(entry), 0o644)
}
