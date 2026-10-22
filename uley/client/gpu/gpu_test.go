package gpu

import "testing"

func TestParseDeviceList(t *testing.T) {
	out := `ggml_vulkan: Found 2 Vulkan devices:
Available devices:
  Vulkan0: NVIDIA GeForce RTX 3060 (12288 MiB, 11593 MiB free)
  Vulkan1: AMD Radeon RX 6700 XT (12272 MiB, 12000 MiB free)
  MTL0: Apple M2 (10922 MiB, 10922 MiB free)
`
	g := ParseDeviceList(out)
	if len(g) != 3 {
		t.Fatalf("got %+v", g)
	}
	if g[0].Vendor != "nvidia" || g[0].VRAMMB != 12288 || g[0].Backend != "vulkan" || g[0].Name != "NVIDIA GeForce RTX 3060" {
		t.Errorf("bad first: %+v", g[0])
	}
	if g[0].Device != "Vulkan0" || g[2].Device != "MTL0" {
		t.Errorf("bad device names: %+v", g)
	}
	if g[1].Vendor != "amd" || g[2].Vendor != "apple" || g[2].Backend != "metal" {
		t.Errorf("bad vendors: %+v", g)
	}
}

func TestFake(t *testing.T) {
	t.Setenv("ULEY_FAKE_GPU", "Emu:amd:8192")
	g := Detect("")
	if len(g) != 1 || g[0].VRAMMB != 8192 || g[0].Vendor != "amd" {
		t.Fatalf("got %+v", g)
	}
}

func TestBudgetLeavesRoomForOtherPrograms(t *testing.T) {
	gpus := ParseDeviceList("  Vulkan0: NVIDIA GeForce RTX 3060 (12288 MiB, 4096 MiB free)\n")
	if gpus[0].FreeMB != 4096 {
		t.Fatalf("free = %d", gpus[0].FreeMB)
	}
	// Ollama holds 8 GB: 70% of the card would be 8601, only 90% of free fits.
	if b := Budget(gpus, 70); b != 3686 {
		t.Fatalf("budget = %d, want 3686", b)
	}
	gpus[0].FreeMB = 12000
	if b := Budget(gpus, 70); b != 12288*70/100 {
		t.Fatalf("idle card budget = %d", b)
	}
	gpus[0].FreeMB = 0 // unknown
	if b := Budget(gpus, 50); b != 6144 {
		t.Fatalf("unknown free budget = %d", b)
	}
}
