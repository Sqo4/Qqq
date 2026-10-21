// Package models is the built-in whitelist of models a Uley node may
// download and run. It is compiled into the client: the coordinator can only
// refer to a model by id, it can never point a node at an arbitrary file.
// Adding a model means shipping a new (signed) client release.
package models

import "sort"

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	File        string `json:"file"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	MinVRAMMB   int    `json:"min_vram_mb"` // weights + KV cache for ContextSize
	ContextSize int    `json:"context_size"`
	License     string `json:"license"`
	// Chain models are too big for one gamer GPU: the coordinator keeps
	// the file and spreads the layers over a chain of volunteer GPUs.
	// For them MinVRAMMB is the total video memory the chain needs.
	Chain bool `json:"chain,omitempty"`
}

// Hashes are the LFS sha256 values published by Hugging Face for the
// official Qwen GGUF repositories.
var whitelist = []Model{
	{
		ID:          "qwen3-0.6b",
		Name:        "Qwen3 0.6B (Q8_0)",
		File:        "Qwen3-0.6B-Q8_0.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf",
		SHA256:      "9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031",
		SizeBytes:   639446688,
		MinVRAMMB:   1536,
		ContextSize: 4096,
		License:     "Apache-2.0",
	},
	{
		ID:          "qwen3-1.7b",
		Name:        "Qwen3 1.7B (Q8_0)",
		File:        "Qwen3-1.7B-Q8_0.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen3-1.7B-GGUF/resolve/main/Qwen3-1.7B-Q8_0.gguf",
		SHA256:      "061b54daade076b5d3362dac252678d17da8c68f07560be70818cace6590cb1a",
		SizeBytes:   1834426016,
		MinVRAMMB:   3072,
		ContextSize: 4096,
		License:     "Apache-2.0",
	},
	{
		ID:          "qwen3-4b",
		Name:        "Qwen3 4B (Q4_K_M)",
		File:        "Qwen3-4B-Q4_K_M.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen3-4B-GGUF/resolve/main/Qwen3-4B-Q4_K_M.gguf",
		SHA256:      "7485fe6f11af29433bc51cab58009521f205840f5b4ae3a32fa7f92e8534fdf5",
		SizeBytes:   2497280256,
		MinVRAMMB:   4096,
		ContextSize: 4096,
		License:     "Apache-2.0",
	},
	{
		ID:          "qwen3-8b",
		Name:        "Qwen3 8B (Q4_K_M)",
		File:        "Qwen3-8B-Q4_K_M.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q4_K_M.gguf",
		SHA256:      "d98cdcbd03e17ce47681435b5150e34c1417f50b5c0019dd560e4882c5745785",
		SizeBytes:   5027783488,
		MinVRAMMB:   6656,
		ContextSize: 4096,
		License:     "Apache-2.0",
	},
	{
		ID:          "qwen3-32b",
		Name:        "Qwen3 32B (Q4_K_M), общая модель улья",
		File:        "Qwen3-32B-Q4_K_M.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen3-32B-GGUF/resolve/main/Qwen3-32B-Q4_K_M.gguf",
		SHA256:      "efd971561896866f0e910cce52761ca77b1b138090c7f15fe284676d57d1f689",
		SizeBytes:   19762149024,
		MinVRAMMB:   25600, // weights + KV cache for 4 parallel chats + buffers
		ContextSize: 16384, // shared by 4 parallel chats
		License:     "Apache-2.0",
		Chain:       true,
	},
}

// All returns the whitelist ordered from the smallest to the largest model.
func All() []Model {
	out := append([]Model(nil), whitelist...)
	sort.Slice(out, func(i, j int) bool { return out[i].MinVRAMMB < out[j].MinVRAMMB })
	return out
}

func Get(id string) (Model, bool) {
	for _, m := range whitelist {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// Best picks the largest model that fits into vramMB. If preferred is a
// whitelisted model that fits, it wins. ok is false when nothing fits.
func Best(vramMB int, preferred string) (Model, bool) {
	if m, found := Get(preferred); found && !m.Chain && m.MinVRAMMB <= vramMB {
		return m, true
	}
	var best Model
	ok := false
	for _, m := range All() {
		if !m.Chain && m.MinVRAMMB <= vramMB {
			best, ok = m, true
		}
	}
	return best, ok
}
