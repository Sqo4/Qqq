// Command chaindemo runs a local hive with a real small model split over
// three emulated volunteers, for screenshots and demos.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sqo4/uley/client/agent"
	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/client/engine"
	"github.com/sqo4/uley/coordinator"
	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
	"github.com/sqo4/uley/web"
)

type idle struct{}

func (idle) IdleTime() (time.Duration, bool) { return time.Hour, true }
func (idle) Fullscreen() bool                { return false }
func (idle) OnBattery() bool                 { return false }

func main() {
	bin, modelPath, dir := os.Args[1], os.Args[2], os.Args[3]
	store, err := coordinator.OpenStore(filepath.Join(dir, "hive.db"))
	if err != nil {
		log.Fatal(err)
	}
	model := models.Model{ID: "qwen3-0.6b", Name: "Qwen3 0.6B (демо)", Chain: true, MinVRAMMB: 3000, ContextSize: 8192, Layers: 28}
	chain, err := coordinator.NewChainManager(store, coordinator.ChainConfig{
		Model: model, ModelPath: modelPath, HeadBin: filepath.Join(bin, "llama-server"), LogDir: dir,
		Parallel: 2, MinRounds: 3, MeasureEvery: 2 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := coordinator.NewServer(store, coordinator.Config{HiveModel: model.ID, Chain: chain, Web: web.FS})
	ctx := context.Background()
	go srv.Run(ctx)
	go http.ListenAndServe("127.0.0.1:18095", srv)
	time.Sleep(time.Second)

	gpus := []struct {
		name, vendor string
		mb           int
	}{{"NVIDIA GeForce RTX 3060", "nvidia", 2400}, {"AMD Radeon RX 6600", "amd", 2300}, {"Intel Arc A380", "intel", 2286}}
	for i, g := range gpus {
		g := g
		cfg, _ := config.Open(filepath.Join(dir, "node", string(rune('a'+i))), "http://127.0.0.1:18095")
		cfg.Update(func(s *config.Settings) {
			s.Consent = config.ConsentVersion
			s.NodeName = []string{"Комп Влада", "Ноутбук Ани", "ПК в общаге"}[i]
		})
		a := &agent.Agent{
			Config: cfg, Engine: &engine.Fake{}, Monitor: idle{}, Version: "demo",
			DetectGPU: func() []protocol.GPU { return []protocol.GPU{{Name: g.name, Vendor: g.vendor, VRAMMB: g.mb}} },
			RPC:       &engine.RPCServer{Bin: filepath.Join(bin, "ggml-rpc-server"), CacheDir: filepath.Join(dir, "cache", g.vendor)},
		}
		go a.Run(ctx)
	}
	log.Print("demo hive on http://127.0.0.1:18095")
	select {}
}
