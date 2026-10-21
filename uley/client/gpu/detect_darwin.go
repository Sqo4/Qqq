package gpu

import (
	"strconv"
	"strings"

	"github.com/sqo4/uley/internal/protocol"
)

// Apple Silicon shares memory between CPU and GPU; Metal lets the GPU use
// roughly two thirds of it.
func detectOS() []protocol.GPU {
	brand, _ := run("sysctl", "-n", "machdep.cpu.brand_string")
	memStr, err := run("sysctl", "-n", "hw.memsize")
	if err != nil || !strings.Contains(brand, "Apple") {
		return nil
	}
	b, _ := strconv.ParseInt(strings.TrimSpace(memStr), 10, 64)
	return []protocol.GPU{{Name: strings.TrimSpace(brand), Vendor: "apple", VRAMMB: int(b>>20) * 2 / 3, Backend: "metal"}}
}
