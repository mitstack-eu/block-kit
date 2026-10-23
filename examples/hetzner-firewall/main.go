// Command hetzner-firewall is an example block operator that keeps one empty Hetzner Cloud firewall for each subscription.
package main

import (
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
