// Command uley is the Uley volunteer client: a tray icon that lends the
// GPU to the Uley hive while the computer is idle, with the owner's consent.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/sqo4/uley/client/agent"
	"github.com/sqo4/uley/client/autostart"
	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/client/download"
	"github.com/sqo4/uley/client/engine"
	"github.com/sqo4/uley/client/gpu"
	"github.com/sqo4/uley/client/sysmon"
	"github.com/sqo4/uley/client/tray"
	"github.com/sqo4/uley/client/ui"
	"github.com/sqo4/uley/client/update"
	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
	"github.com/sqo4/uley/internal/signing"
)

// Set at build time with -ldflags "-X main.version=... ".
var (
	version            = "dev"
	defaultCoordinator = "http://127.0.0.1:8080"
	updatePublicKey    = ""
	updateManifestURL  = "https://github.com/Sqo4/Qqq/releases/latest/download/update-{os}-{arch}.json"
	downloadPage       = "https://github.com/Sqo4/Qqq/releases/latest"
)

func main() {
	background := flag.Bool("background", false, "start without opening the panel (used by autostart)")
	headless := flag.Bool("headless", false, "no tray icon; for servers. Use with -accept")
	accept := flag.Bool("accept", false, "headless: agree to the terms shown by -terms")
	terms := flag.Bool("terms", false, "print what Uley does and exit")
	coordinator := flag.String("coordinator", "", "coordinator URL to use and remember")
	fake := flag.Bool("fake-engine", os.Getenv("ULEY_ENGINE") == "fake", "emulate the GPU (for testing)")
	insecure := flag.Bool("insecure", false, "allow a plain-http coordinator on another host (testing only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *terms {
		fmt.Print(termsText)
		return
	}

	dir, err := config.Dir()
	if err != nil {
		log.Fatal(err)
	}
	// A second launch (e.g. double-clicking the shortcut) just opens the
	// panel of the copy that is already running.
	if url := ui.FindRunning(dir); url != "" {
		if !*headless {
			ui.OpenBrowser(url)
		}
		return
	}
	setupLog(dir, *headless)

	cfg, err := config.Open(dir, defaultCoordinator)
	if err != nil {
		log.Fatalf("settings: %v", err)
	}
	if *coordinator != "" {
		if err := agent.CheckCoordinatorURL(*coordinator, *insecure); err != nil {
			log.Fatal(err)
		}
		cfg.Update(func(s *config.Settings) { s.Coordinator = *coordinator })
	}
	if *headless && *accept {
		cfg.Update(func(s *config.Settings) { s.Consent = config.ConsentVersion })
	}

	var eng engine.Engine
	enginePath := ""
	if *fake {
		eng = &engine.Fake{Delay: 2 * time.Second}
		if os.Getenv("ULEY_FAKE_GPU") == "" {
			os.Setenv("ULEY_FAKE_GPU", "Эмулятор GPU:other:8192")
		}
	} else {
		enginePath = engine.FindLlamaServer()
		eng = &engine.Llama{Bin: enginePath, LogDir: filepath.Join(dir, "logs")}
	}
	ag := &agent.Agent{
		Config: cfg, Engine: eng, Monitor: sysmon.System{}, Version: version,
		DetectGPU:     func() []protocol.GPU { return gpu.Detect(enginePath) },
		ModelsDir:     filepath.Join(dir, "models"),
		AllowInsecure: *insecure,
	}
	if *fake {
		// The emulator needs no model files.
		ag.Fetch = func(ctx context.Context, m models.Model, p download.Progress) (string, error) { return m.File, nil }
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan struct{})
	go func() { ag.Run(ctx); close(done) }()

	upd := newUpdater(dir)
	go upd.loop(ctx)

	panel := &ui.Server{
		Agent: ag, Config: cfg, Version: version,
		Autostart:       autostart.Set,
		UpdateAvailable: upd.available,
		OnUpdate:        func() error { return upd.install(ctx, cancel) },
	}
	if err := panel.Start(); err != nil {
		log.Fatalf("panel: %v", err)
	}
	defer panel.Close()
	log.Printf("uley %s started, panel at %s", version, panel.URL())

	shutdown := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
	if *headless {
		if cfg.Get().Consent < config.ConsentVersion {
			log.Printf("No consent yet. Read `uley -terms`, then run with -accept, or open %s", panel.URL())
		}
		<-ctx.Done()
		shutdown()
		return
	}

	open := func() {
		if err := ui.OpenBrowser(panel.URL()); err != nil {
			log.Print(err)
		}
	}
	// First start: show the consent page right away.
	if !*background || cfg.Get().Consent < config.ConsentVersion {
		open()
	}
	go func() {
		<-ctx.Done()
		quitTray()
	}()
	tray.Run(tray.App{Agent: ag, Config: cfg, OpenPanel: open, UpdateAvailable: upd.available, OnQuit: shutdown})
}

func setupLog(dir string, toStderr bool) {
	logDir := filepath.Join(dir, "logs")
	os.MkdirAll(logDir, 0o700)
	path := filepath.Join(logDir, "uley.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if toStderr {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	} else {
		log.SetOutput(f)
	}
}

// updater checks for signed updates every few hours.
type updater struct {
	checker update.Checker
	dir     string
	mu      sync.Mutex
	pending *signing.Manifest
}

func newUpdater(dir string) *updater {
	return &updater{dir: filepath.Join(dir, "updates"), checker: update.Checker{
		ManifestURL: updateManifestURL, PublicKey: updatePublicKey, Current: version,
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}}
}

func (u *updater) loop(ctx context.Context) {
	for {
		m, err := u.checker.Check(ctx)
		if err != nil {
			log.Printf("update check: %v", err)
		}
		if m != nil {
			u.mu.Lock()
			u.pending = m
			u.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

func (u *updater) available() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.pending == nil {
		return ""
	}
	return u.pending.Version
}

// install downloads the verified installer and runs it (Windows), or opens
// the download page (other systems, where packages are updated by hand).
func (u *updater) install(ctx context.Context, stop context.CancelFunc) error {
	u.mu.Lock()
	m := u.pending
	u.mu.Unlock()
	if m == nil {
		return fmt.Errorf("нет обновлений")
	}
	if runtime.GOOS != "windows" {
		return ui.OpenBrowser(downloadPage)
	}
	path, err := u.checker.Download(ctx, m, u.dir)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART")
	if err := cmd.Start(); err != nil {
		return err
	}
	// The installer closes us and starts the new version.
	stop()
	return nil
}

const termsText = `Улей — добровольная распределённая нейросеть.

Программа использует видеокарту этого компьютера, чтобы отвечать на
текстовые запросы к открытым языковым моделям:
  * скачивает модель только из встроенного списка и проверяет SHA-256;
  * получает текстовые задания с координатора по HTTPS и возвращает ответы;
  * отправляет только модель и объём памяти видеокарты, ОС и статус задач;
  * не запускает чужие программы и не майнит;
  * по умолчанию работает, только когда компьютер простаивает, и
    останавливается при полноэкранной игре и работе от батареи.

Согласие: uley -headless -accept
`
