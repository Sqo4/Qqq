// Package gpu finds out which video cards the computer has and how much
// video memory they carry. Only the card model, vendor and memory size are
// ever sent to the coordinator.
package gpu

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sqo4/uley/client/procutil"
	"github.com/sqo4/uley/internal/protocol"
)

// Detect lists GPUs. enginePath is the llama-server binary; when present its
// own device list is the most reliable source, because it shows exactly
// what the engine will be able to use.
func Detect(enginePath string) []protocol.GPU {
	if fake := os.Getenv("ULEY_FAKE_GPU"); fake != "" {
		return parseFake(fake)
	}
	if enginePath != "" {
		if gpus := fromEngine(enginePath); len(gpus) > 0 {
			return gpus
		}
	}
	return detectOS()
}

// parseFake reads "Name:vendor:VRAMMB", used by tests and the emulator.
func parseFake(s string) []protocol.GPU {
	parts := strings.Split(s, ":")
	g := protocol.GPU{Name: parts[0], Vendor: "other", Backend: "fake"}
	if len(parts) > 1 {
		g.Vendor = parts[1]
	}
	if len(parts) > 2 {
		g.VRAMMB, _ = strconv.Atoi(parts[2])
	}
	return []protocol.GPU{g}
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	procutil.Quiet(cmd)
	out, err := cmd.Output()
	return string(out), err
}

func fromEngine(path string) []protocol.GPU {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--list-devices")
	procutil.Quiet(cmd)
	out, _ := cmd.CombinedOutput()
	return ParseDeviceList(string(out))
}

// Lines look like "  Vulkan0: NVIDIA GeForce RTX 3060 (12288 MiB, 11593 MiB free)".
var deviceLine = regexp.MustCompile(`(?m)^\s*(([A-Za-z]+)\d+):\s*(.+?)\s*\((\d+) MiB`)

func ParseDeviceList(out string) []protocol.GPU {
	var gpus []protocol.GPU
	for _, m := range deviceLine.FindAllStringSubmatch(out, -1) {
		mb, _ := strconv.Atoi(m[4])
		backend := strings.ToLower(m[2])
		if backend == "mtl" {
			backend = "metal"
		}
		gpus = append(gpus, protocol.GPU{Device: m[1], Name: m[3], Vendor: VendorOf(m[3]), VRAMMB: mb, Backend: backend})
	}
	return gpus
}

func VendorOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia"), strings.Contains(n, "geforce"), strings.Contains(n, "rtx"),
		strings.Contains(n, "gtx"), strings.Contains(n, "quadro"), strings.Contains(n, "tesla"):
		return "nvidia"
	case strings.Contains(n, "amd"), strings.Contains(n, "radeon"), strings.Contains(n, "ati "):
		return "amd"
	case strings.Contains(n, "intel"), strings.Contains(n, " arc"), strings.Contains(n, "iris"), strings.Contains(n, "uhd"):
		return "intel"
	case strings.Contains(n, "apple"):
		return "apple"
	}
	return "other"
}

// fromNvidiaSMI works everywhere the NVIDIA driver is installed.
func fromNvidiaSMI() []protocol.GPU {
	out, err := run("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	var gpus []protocol.GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, mem, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		mb, _ := strconv.Atoi(strings.TrimSpace(mem))
		gpus = append(gpus, protocol.GPU{Name: strings.TrimSpace(name), Vendor: "nvidia", VRAMMB: mb})
	}
	return gpus
}

// Usable returns the biggest VRAM among cards that can run models.
func Usable(gpus []protocol.GPU) int {
	best := 0
	for _, g := range gpus {
		best = max(best, g.VRAMMB)
	}
	return best
}
