// Command mitstack-signin is the sign-in companion a Docker host runs beside its edge, which asks it about every
// request to an app that opens only to its org's members.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/signin"
)

func main() {
	listen := flag.String("listen", ":"+signin.Port, "the address to serve the edge on")
	data := flag.String("data", "/data", "the directory that keeps the companion's key across restarts")
	platform := flag.String("platform", "https://api.mitstack.dev", "mitstack's API origin")
	caFile := flag.String("ca", "/mitstack-signin/ca-certificates.crt", "the CA bundle that verifies mitstack's certificate")
	version := flag.Bool("version", false, "print the program's name and Go version, and exit")
	flag.Parse()
	if *version {
		fmt.Println("mitstack-signin " + runtime.Version())
		return
	}
	if err := run(*listen, *data, *platform, *caFile); err != nil {
		log.Fatal(err)
	}
}

func run(listen, data, platform, caFile string) error {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("%s holds no certificate", caFile)
	}
	key, err := signin.LoadKey(filepath.Join(data, "key"))
	if err != nil {
		return err
	}
	handler, err := signin.New(signin.Config{
		Platform: platform,
		Key:      key,
		Client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		},
		Logf: log.Printf,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	log.Printf("mitstack-signin serves %s for %s", listen, platform)
	return server.ListenAndServe()
}
