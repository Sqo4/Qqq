package coordinator

import (
	"math"
	"sort"
)

// BalanceInput describes one chain worker for speed balancing.
type BalanceInput struct {
	ID        string
	MaxLayers int     // layers that fit into its video memory budget
	LayerMS   float64 // measured compute time per layer per round; 0 = unknown
	NetMS     float64 // network round trip to the coordinator
}

// Balance splits total layers so that every worker spends about the same
// time per round (per word): NetMS + layers*LayerMS is equalised, never
// giving a worker more than fits into its memory. A worker so slow (or so
// far away) that even one layer would hold everybody up gets 0 layers and
// stays a spare. ok is false when the memory of all workers is not enough.
//
// Workers without a measurement yet are assumed to be as fast as the
// median of the measured ones; with no measurements at all the split
// follows memory, as before balancing existed.
func Balance(in []BalanceInput, total int) (layers []int, roundMS float64, ok bool) {
	layers = make([]int, len(in))
	capacity := 0
	for _, w := range in {
		capacity += max(w.MaxLayers, 0)
	}
	if total <= 0 || capacity < total {
		return layers, 0, false
	}
	cost := measuredCosts(in)
	if cost == nil {
		return byMemory(in, total), 0, true
	}

	// Water-filling: find the round time T at which the workers together
	// take exactly `total` layers.
	share := func(T float64, i int) float64 {
		l := (T - in[i].NetMS) / cost[i]
		return math.Min(math.Max(l, 0), float64(max(in[i].MaxLayers, 0)))
	}
	sum := func(T float64) float64 {
		s := 0.0
		for i := range in {
			s += share(T, i)
		}
		return s
	}
	lo, hi := 0.0, 1.0
	for sum(hi) < float64(total) {
		hi *= 2
	}
	for iter := 0; iter < 100; iter++ {
		mid := (lo + hi) / 2
		if sum(mid) < float64(total) {
			lo = mid
		} else {
			hi = mid
		}
	}
	given := 0
	for i := range in {
		layers[i] = int(math.Floor(share(hi, i) + 1e-9))
		given += layers[i]
	}
	// Hand out the rounding leftovers where they cost the least time.
	for given < total {
		best, bestT := -1, math.Inf(1)
		for i := range in {
			if layers[i] >= in[i].MaxLayers {
				continue
			}
			t := in[i].NetMS + float64(layers[i]+1)*cost[i]
			if t < bestT {
				best, bestT = i, t
			}
		}
		layers[best]++
		given++
	}
	for given > total { // floating point safety
		for i := range layers {
			if layers[i] > 0 && given > total {
				layers[i]--
				given--
			}
		}
	}
	return layers, RoundTime(in, layers, cost), true
}

// RoundTime is the time per round of the slowest worker in a split.
func RoundTime(in []BalanceInput, layers []int, cost []float64) float64 {
	worst := 0.0
	for i, l := range layers {
		if l == 0 {
			continue
		}
		c := in[i].LayerMS
		if cost != nil {
			c = cost[i]
		}
		worst = math.Max(worst, in[i].NetMS+float64(l)*c)
	}
	return worst
}

func byMemory(in []BalanceInput, total int) []int {
	layers := make([]int, len(in))
	capacity := 0
	for _, w := range in {
		capacity += w.MaxLayers
	}
	given := 0
	for i, w := range in {
		layers[i] = w.MaxLayers * total / capacity
		given += layers[i]
	}
	for i := 0; given < total; i = (i + 1) % len(in) {
		if layers[i] < in[i].MaxLayers {
			layers[i]++
			given++
		}
	}
	return layers
}

// Fastest minimises the time per word. Layers run strictly one worker after
// another, so a word costs the SUM of every used worker's network trip and
// compute: Σ (NetMS + layers*LayerMS). The cheapest way is to fill the
// fastest cards up to their memory and use as few workers as possible;
// slow or far-away ones stay spares. It returns 0 layers for spares.
func Fastest(in []BalanceInput, total int) (layers []int, wordMS float64, ok bool) {
	layers = make([]int, len(in))
	capacity := 0
	for _, w := range in {
		capacity += max(w.MaxLayers, 0)
	}
	if total <= 0 || capacity < total {
		return layers, 0, false
	}
	cost := measuredCosts(in)
	unknown := cost == nil
	if unknown {
		// Nobody measured yet: treat all cards alike, which means using the
		// fewest computers with the most memory and the best connection.
		cost = make([]float64, len(in))
		for i := range cost {
			cost[i] = 1
		}
	}
	// Which workers to use: cheapest per layer once the network trip is
	// spread over everything the worker can hold.
	order := make([]int, len(in))
	for i := range order {
		order[i] = i
	}
	score := func(i int) float64 { return cost[i] + in[i].NetMS/float64(max(in[i].MaxLayers, 1)) }
	sort.SliceStable(order, func(a, b int) bool {
		if sa, sb := score(order[a]), score(order[b]); sa != sb {
			return sa < sb
		}
		return in[order[a]].MaxLayers > in[order[b]].MaxLayers // fewer, bigger cards
	})
	var chosen []int
	room := 0
	for _, i := range order {
		if room >= total {
			break
		}
		if in[i].MaxLayers <= 0 {
			continue
		}
		chosen = append(chosen, i)
		room += in[i].MaxLayers
	}
	// Among the chosen, the fastest cards fill up first.
	sort.SliceStable(chosen, func(a, b int) bool {
		if cost[chosen[a]] != cost[chosen[b]] {
			return cost[chosen[a]] < cost[chosen[b]]
		}
		return in[chosen[a]].MaxLayers > in[chosen[b]].MaxLayers
	})
	left := total
	for _, i := range chosen {
		l := min(in[i].MaxLayers, left)
		layers[i] = l
		left -= l
	}
	if unknown {
		return layers, 0, true
	}
	return layers, WordTime(in, layers, cost), true
}

// WordTime is the time per word of a split: the sum over used workers.
func WordTime(in []BalanceInput, layers []int, cost []float64) float64 {
	sum := 0.0
	for i, l := range layers {
		if l == 0 {
			continue
		}
		c := in[i].LayerMS
		if cost != nil {
			c = cost[i]
		}
		sum += in[i].NetMS + float64(l)*c
	}
	return sum
}

// measuredCosts fills unknown speeds with the median; nil if none is known.
func measuredCosts(in []BalanceInput) []float64 {
	var known []float64
	for _, w := range in {
		if w.LayerMS > 0 {
			known = append(known, w.LayerMS)
		}
	}
	if len(known) == 0 {
		return nil
	}
	sort.Float64s(known)
	median := known[len(known)/2]
	cost := make([]float64, len(in))
	for i, w := range in {
		cost[i] = w.LayerMS
		if cost[i] <= 0 {
			cost[i] = median
		}
	}
	return cost
}
