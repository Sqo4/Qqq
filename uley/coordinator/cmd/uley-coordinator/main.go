// Command uley-coordinator runs the central server of the Uley hive.
//
// Local test:   uley-coordinator -addr 127.0.0.1:8080 -db uley.db
// Production:   uley-coordinator -domain hive.example.org -db /var/lib/uley/uley.db -api-keys KEY1,KEY2
// With -domain the server gets a free Let's Encrypt certificate itself and
// listens on ports 80 and 443.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sqo4/uley/client/download"
	"github.com/sqo4/uley/coordinator"
	"github.com/sqo4/uley/internal/datadir"
	"github.com/sqo4/uley/internal/lan"
	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/web"
	"golang.org/x/crypto/acme/autocert"
)

var version = "dev"

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// fetchModel downloads the chain model (resuming, SHA-256 checked) and
// hands it to the chain manager.
func fetchModel(ctx context.Context, chain *coordinator.ChainManager, m models.Model, dir string) {
	last := time.Time{}
	for ctx.Err() == nil {
		p, err := download.Fetch(ctx, &http.Client{}, dir, m, func(done, total int64) {
			if time.Since(last) > 5*time.Second {
				last = time.Now()
				chain.SetMessage(fmt.Sprintf("Сервер скачивает модель: %d%%", done*100/total))
			}
		})
		if err == nil {
			log.Printf("model %s ready at %s", m.ID, p)
			chain.SetModelPath(p)
			return
		}
		log.Printf("model download: %v (retrying in a minute)", err)
		if datadir.IsDiskFull(err) {
			log.Printf("На диске не хватает места для модели (%.1f ГБ). Переустановите Улей и выберите папку для моделей на другом диске или запустите сервер с -model-dir D:\\Uley", float64(m.SizeBytes)/(1<<30))
			chain.SetMessage("На сервере не хватает места на диске для модели")
		} else {
			chain.SetMessage("Не удалось скачать модель, пробуем снова: " + err.Error())
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
	}
}

// printLANAddresses tells, in plain words, which address the other
// computers of a local network should use.
func printLANAddresses(addr string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || (host != "0.0.0.0" && host != "" && host != "::") {
		return
	}
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || !lan.IsHome(ipn.IP) {
			continue
		}
		if lan.IsVPN(ipn.IP) {
			log.Printf("Улей в VPN: друзья в той же VPN открывают http://%s:%s и указывают этот адрес в настройках программы Улей", ipn.IP, port)
			continue
		}
		log.Printf("Улей в локальной сети: откройте на других компьютерах http://%s:%s и укажите этот адрес в настройках программы Улей", ipn.IP, port)
	}
}

func findLlamaServer() string {
	name := "llama-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "engine", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p, _ := exec.LookPath(name)
	return p
}

func main() {
	addr := flag.String("addr", env("ULEY_ADDR", "127.0.0.1:8080"), "listen address (ignored with -domain)")
	domain := flag.String("domain", env("ULEY_DOMAIN", ""), "public domain; enables automatic HTTPS via Let's Encrypt")
	dbPath := flag.String("db", env("ULEY_DB", "uley.db"), "SQLite database file")
	apiKeys := flag.String("api-keys", env("ULEY_API_KEYS", ""), "comma separated API keys allowed to submit tasks (empty = open, for testing only)")
	requireKey := flag.Bool("require-api-key", os.Getenv("ULEY_REQUIRE_API_KEY") != "0", "close /v1 and the task API to callers without a key (admin key or a personal key from the web app); set false only for local tests")
	tgToken := flag.String("telegram-token", env("ULEY_TELEGRAM_TOKEN", ""), "Telegram bot token from @BotFather; enables the Telegram bot")
	tz := flag.String("tz", env("ULEY_TZ", "Europe/Moscow"), "time zone for scheduled tasks")
	signupKey := flag.String("signup-key", env("ULEY_SIGNUP_KEY", ""), "optional code people must enter to create an account (e.g. for a university)")
	joinKey := flag.String("join-key", env("ULEY_JOIN_KEY", ""), "optional key nodes must know to register")
	verify := flag.Float64("verify-rate", 0.1, "share of temperature-0 tasks computed twice for verification")
	jobTimeout := flag.Duration("job-timeout", 3*time.Minute, "time a node has to finish a job")
	hiveModel := flag.String("model", env("ULEY_MODEL", "qwen3-32b"), "the one model the hive serves (see /api/v1/models); a chain model is split over volunteer GPUs")
	modelDir := flag.String("model-dir", env("ULEY_MODEL_DIR", ""), "where the coordinator keeps a chain model (default: models/ next to the database)")
	balance := flag.String("chain-balance", env("ULEY_CHAIN_BALANCE", "fastest"), "how layers are split over volunteers: fastest (least time per word) or equal (same time on every link)")
	headBin := flag.String("llama-server", env("ULEY_LLAMA_SERVER", ""), "llama-server binary that drives the chain (default: engine/ next to this program, then PATH)")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o700); err != nil {
		log.Fatal(err)
	}
	store, err := coordinator.OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	var keys []string
	for _, k := range strings.Split(*apiKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 && !*requireKey {
		log.Printf("WARNING: -require-api-key=false and no -api-keys: anyone can submit tasks")
	}
	model, ok := models.Get(*hiveModel)
	if !ok {
		log.Fatalf("unknown model %q", *hiveModel)
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		log.Fatalf("time zone %q: %v", *tz, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := coordinator.Config{
		APIKeys: keys, JoinKey: *joinKey, VerifyRate: *verify, JobTimeout: *jobTimeout, Web: web.FS,
		Location: loc, HiveModel: model.ID, SignupKey: *signupKey, RequireAPIKey: *requireKey, TelegramToken: *tgToken,
	}
	if model.Chain {
		head := *headBin
		if head == "" {
			head = findLlamaServer()
		}
		if head == "" {
			log.Fatal("chain model needs llama-server: put it into engine/ next to this program or pass -llama-server")
		}
		dir := *modelDir
		if dir == "" {
			if d := datadir.Get(); d != "" {
				dir = filepath.Join(d, "server-models")
			} else {
				dir = filepath.Join(filepath.Dir(*dbPath), "models")
			}
		}
		log.Printf("модель будет храниться в %s", dir)
		chain, err := coordinator.NewChainManager(store, coordinator.ChainConfig{
			Model: model, HeadBin: head, LogDir: filepath.Join(filepath.Dir(*dbPath), "logs"), Balance: *balance,
		})
		if err != nil {
			log.Fatal(err)
		}
		cfg.Chain = chain
		go fetchModel(ctx, chain, model, dir)
	}
	srv := coordinator.NewServer(store, cfg)
	go srv.Run(ctx)

	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()

	log.Printf("uley-coordinator %s", version)
	if *domain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
			Cache:      autocert.DirCache(filepath.Join(filepath.Dir(*dbPath), "certs")),
		}
		go func() {
			// Port 80 answers Let's Encrypt challenges and redirects to HTTPS.
			log.Print(http.ListenAndServe(":80", m.HTTPHandler(nil)))
		}()
		hs.Addr = ":443"
		hs.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate, MinVersion: tls.VersionTLS12}
		log.Printf("listening on https://%s", *domain)
		err = hs.ListenAndServeTLS("", "")
	} else {
		hs.Addr = *addr
		log.Printf("listening on http://%s (plain HTTP: put it behind HTTPS before going public)", *addr)
		printLANAddresses(*addr)
		err = hs.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
