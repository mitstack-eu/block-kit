package main

import (
	_ "embed"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/webhook"
)

const (
	listenAddr        = ":8080"
	readHeaderTimeout = 10 * time.Second
)

//go:embed manifest.yaml
var manifest []byte

//go:embed type-snapshot.json
var typeSnapshot []byte

func main() {
	logs := webhook.NewLogBuffer(0)
	log.SetOutput(io.MultiWriter(os.Stderr, logs.Writer()))
	cfg, err := newConfig(newHetzner(hetznerAPI), logs)
	if err != nil {
		log.Fatal(err)
	}
	cfg.JWKSURL = os.Getenv("MITSTACK_JWKS_URL")
	h, err := handler.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: listenAddr, Handler: h, ReadHeaderTimeout: readHeaderTimeout}
	log.Fatal(server.ListenAndServe())
}
