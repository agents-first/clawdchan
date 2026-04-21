// Command clawdchan-relay is the reference WebSocket relay. It wraps
// internal/relayserver with a minimal ListenAndServe.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/agents-first/ClawdChan/internal/relayserver"
)

func main() {
	addr := flag.String("addr", ":8787", "listen address")
	flag.Parse()

	r := relayserver.New(relayserver.Config{})
	log.Printf("clawdchan-relay listening on %s", *addr)
	if err := http.ListenAndServe(*addr, r.Handler()); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
