package gpu

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sqo4/uley/internal/protocol"
)

func detectOS() []protocol.GPU {
	if g := fromNvidiaSMI(); len(g) > 0 {
		return g
	}
	var gpus []protocol.GPU
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*/device")
	for _, dev := range cards {
		vendorID := readTrim(filepath.Join(dev, "vendor"))
		var g protocol.GPU
		switch vendorID {
		case "0x1002":
			g.Vendor, g.Name = "amd", "AMD Radeon"
		case "0x8086":
			g.Vendor, g.Name = "intel", "Intel GPU"
		case "0x10de":
			g.Vendor, g.Name = "nvidia", "NVIDIA GPU"
		default:
			continue
		}
		if b, err := strconv.ParseInt(readTrim(filepath.Join(dev, "mem_info_vram_total")), 10, 64); err == nil {
			g.VRAMMB = int(b >> 20)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}
