package coordinator

import (
	"math"
	"testing"
)

func sumInts(v []int) int {
	s := 0
	for _, x := range v {
		s += x
	}
	return s
}

func TestBalanceEqualisesTime(t *testing.T) {
	// A fast card (1 ms per layer) and a slow one (3 ms per layer), same network.
	in := []BalanceInput{
		{ID: "rtx4080", MaxLayers: 64, LayerMS: 1, NetMS: 20},
		{ID: "gtx1660", MaxLayers: 64, LayerMS: 3, NetMS: 20},
	}
	layers, round, ok := Balance(in, 64)
	if !ok || sumInts(layers) != 64 {
		t.Fatalf("bad split %v", layers)
	}
	if layers[0] != 48 || layers[1] != 16 {
		t.Fatalf("want 48/16, got %v", layers)
	}
	t0, t1 := 20+float64(layers[0])*1, 20+float64(layers[1])*3
	if math.Abs(t0-t1) > 3 || round != math.Max(t0, t1) {
		t.Fatalf("times not equal: %.1f vs %.1f (round %.1f)", t0, t1, round)
	}
}

func TestBalanceRespectsMemory(t *testing.T) {
	in := []BalanceInput{
		{ID: "fast-small", MaxLayers: 10, LayerMS: 0.5, NetMS: 10},
		{ID: "slow-big", MaxLayers: 64, LayerMS: 4, NetMS: 10},
	}
	layers, _, ok := Balance(in, 40)
	if !ok || layers[0] != 10 || layers[1] != 30 {
		t.Fatalf("memory cap ignored: %v", layers)
	}
}

func TestBalanceDropsHopelessWorker(t *testing.T) {
	// The third worker is on a terrible connection: one layer there would
	// cost more than the whole round elsewhere, so it stays a spare.
	in := []BalanceInput{
		{ID: "a", MaxLayers: 40, LayerMS: 1, NetMS: 20},
		{ID: "b", MaxLayers: 40, LayerMS: 1, NetMS: 20},
		{ID: "far", MaxLayers: 40, LayerMS: 1, NetMS: 400},
	}
	layers, _, ok := Balance(in, 64)
	if !ok || layers[2] != 0 || layers[0]+layers[1] != 64 {
		t.Fatalf("far worker must be left out: %v", layers)
	}
}

func TestBalanceTenMixedPCs(t *testing.T) {
	// Ten computers of very different power end up within a few ms.
	speeds := []float64{0.6, 0.8, 1, 1.2, 1.5, 2, 2.5, 3, 4, 5}
	var in []BalanceInput
	for i, s := range speeds {
		in = append(in, BalanceInput{ID: string(rune('a' + i)), MaxLayers: 30, LayerMS: s, NetMS: 15 + float64(i)})
	}
	layers, _, ok := Balance(in, 64)
	if !ok || sumInts(layers) != 64 {
		t.Fatalf("bad split %v", layers)
	}
	lo, hi := math.Inf(1), 0.0
	for i, l := range layers {
		if l == 0 {
			continue
		}
		tt := in[i].NetMS + float64(l)*in[i].LayerMS
		lo, hi = math.Min(lo, tt), math.Max(hi, tt)
	}
	if hi-lo > 5.5 { // one layer of the slowest card at most
		t.Fatalf("times spread too wide: %.1f..%.1f ms, layers %v", lo, hi, layers)
	}
	t.Logf("layers %v, every round %.1f..%.1f ms", layers, lo, hi)
}

func TestBalanceUnknownAndInfeasible(t *testing.T) {
	in := []BalanceInput{{ID: "a", MaxLayers: 20}, {ID: "b", MaxLayers: 60}}
	layers, _, ok := Balance(in, 64)
	if !ok || layers[0] != 16 || layers[1] != 48 {
		t.Fatalf("without measurements the split must follow memory: %v", layers)
	}
	if _, _, ok := Balance([]BalanceInput{{ID: "a", MaxLayers: 10}}, 64); ok {
		t.Fatal("not enough memory must be reported")
	}
	// One measured, one not: the unknown one is assumed median-fast.
	in = []BalanceInput{{ID: "a", MaxLayers: 64, LayerMS: 2, NetMS: 10}, {ID: "b", MaxLayers: 64, NetMS: 10}}
	layers, _, _ = Balance(in, 64)
	if layers[0] != 32 || layers[1] != 32 {
		t.Fatalf("unknown worker must count as median: %v", layers)
	}
}

func TestFastestBeatsEqualTime(t *testing.T) {
	in := []BalanceInput{
		{ID: "rtx4080", MaxLayers: 64, LayerMS: 1, NetMS: 20},
		{ID: "gtx1660", MaxLayers: 64, LayerMS: 3, NetMS: 20},
	}
	fl, fastWord, _ := Fastest(in, 64)
	el, _, _ := Balance(in, 64)
	equalWord := WordTime(in, el, nil)
	if fl[0] != 64 || fl[1] != 0 {
		t.Fatalf("the fast card alone holds the model: %v", fl)
	}
	if fastWord != 84 || equalWord != 136 {
		t.Fatalf("word times: fastest %.0f, equal %.0f", fastWord, equalWord)
	}
}

func TestFastestUsesFewestFastCards(t *testing.T) {
	// 64 layers, every card holds 16: four are needed. The four fastest win,
	// the slow ones and the one on a bad connection stay spares.
	var in []BalanceInput
	for i, s := range []float64{0.6, 0.8, 1, 1.2, 1.5, 2, 2.5, 3, 4, 5} {
		in = append(in, BalanceInput{ID: string(rune('a' + i)), MaxLayers: 16, LayerMS: s, NetMS: 20})
	}
	in[1].NetMS = 500 // fast card, terrible connection
	layers, word, ok := Fastest(in, 64)
	if !ok || sumInts(layers) != 64 {
		t.Fatalf("bad split %v", layers)
	}
	want := []int{16, 0, 16, 16, 16, 0, 0, 0, 0, 0}
	for i := range want {
		if layers[i] != want[i] {
			t.Fatalf("want %v, got %v", want, layers)
		}
	}
	t.Logf("word time %.0f ms with %v", word, layers)
}

func TestFastestWithoutMeasurementsUsesFewestComputers(t *testing.T) {
	in := []BalanceInput{{ID: "small", MaxLayers: 10}, {ID: "big1", MaxLayers: 40}, {ID: "big2", MaxLayers: 40}}
	layers, _, ok := Fastest(in, 64)
	if !ok || layers[0] != 0 || layers[1]+layers[2] != 64 {
		t.Fatalf("the two big cards are enough: %v", layers)
	}
}
