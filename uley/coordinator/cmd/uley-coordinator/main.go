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
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sqo4/uley/coordinator"
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

func main() {
	addr := flag.String("addr", env("ULEY_ADDR", "127.0.0.1:8080"), "listen address (ignored with -domain)")
	domain := flag.String("domain", env("ULEY_DOMAIN", ""), "public domain; enables automatic HTTPS via Let's Encrypt")
	dbPath := flag.String("db", env("ULEY_DB", "uley.db"), "SQLite database file")
	apiKeys := flag.String("api-keys", env("ULEY_API_KEYS", ""), "comma separated API keys allowed to submit tasks (empty = open, for testing only)")
	joinKey := flag.String("join-key", env("ULEY_JOIN_KEY", ""), "optional key nodes must know to register")
	verify := flag.Float64("verify-rate", 0.1, "share of temperature-0 tasks computed twice for verification")
	jobTimeout := flag.Duration("job-timeout", 3*time.Minute, "time a node has to finish a job")
	flag.Parse()

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
	if len(keys) == 0 {
		log.Printf("WARNING: no -api-keys set, anyone can submit tasks")
	}
	srv := coordinator.NewServer(store, coordinator.Config{
		APIKeys: keys, JoinKey: *joinKey, VerifyRate: *verify, JobTimeout: *jobTimeout, Web: web.FS,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
		err = hs.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
