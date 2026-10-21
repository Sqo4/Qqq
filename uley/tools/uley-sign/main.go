// Command uley-sign creates the update signing key and signs update
// manifests in CI.
//
//	uley-sign keygen
//	ULEY_UPDATE_PRIVATE_KEY=... uley-sign sign -version 0.2.0 -os windows -arch amd64 -url https://... -file Uley-Setup.exe
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/sqo4/uley/internal/signing"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: uley-sign keygen | sign [flags]")
	}
	switch os.Args[1] {
	case "keygen":
		pub, priv, err := signing.GenerateKey()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Public key (repository variable ULEY_UPDATE_PUBLIC_KEY):\n%s\n\nPrivate key (secret ULEY_UPDATE_PRIVATE_KEY, never share it):\n%s\n", pub, priv)
	case "sign":
		fs := flag.NewFlagSet("sign", flag.ExitOnError)
		version := fs.String("version", "", "release version")
		goos := fs.String("os", "", "windows, linux or darwin")
		arch := fs.String("arch", "amd64", "amd64 or arm64")
		url := fs.String("url", "", "public https URL of the installer")
		file := fs.String("file", "", "local installer file")
		fs.Parse(os.Args[2:])
		f, err := os.Open(*file)
		if err != nil {
			log.Fatal(err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			log.Fatal(err)
		}
		f.Close()
		m, err := signing.Sign(signing.Manifest{
			Version: *version, OS: *goos, Arch: *arch, URL: *url, SHA256: hex.EncodeToString(h.Sum(nil)),
		}, os.Getenv("ULEY_UPDATE_PRIVATE_KEY"))
		if err != nil {
			log.Fatal(err)
		}
		json.NewEncoder(os.Stdout).Encode(m)
	default:
		log.Fatal("unknown command")
	}
}
