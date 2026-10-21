package gpu

import (
	"strconv"
	"strings"

	"github.com/sqo4/uley/internal/protocol"
)

// The display adapter class in the registry keeps the real VRAM size;
// WMI's AdapterRAM is a 32-bit field and stops at 4 GB.
const psScript = `$ErrorActionPreference='SilentlyContinue';` +
	`Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}\0*' |` +
	`Where-Object { $_.DriverDesc } | ForEach-Object { $_.DriverDesc + '|' + [string]$_.'HardwareInformation.qwMemorySize' }`

func detectOS() []protocol.GPU {
	if g := fromNvidiaSMI(); len(g) > 0 {
		return g
	}
	out, err := run("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	if err != nil {
		return nil
	}
	var gpus []protocol.GPU
	for _, line := range strings.Split(out, "\n") {
		name, mem, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || strings.Contains(strings.ToLower(name), "basic display") {
			continue
		}
		b, _ := strconv.ParseInt(strings.TrimSpace(mem), 10, 64)
		gpus = append(gpus, protocol.GPU{Name: name, Vendor: VendorOf(name), VRAMMB: int(b >> 20)})
	}
	return gpus
}
