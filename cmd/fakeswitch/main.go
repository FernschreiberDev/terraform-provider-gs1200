// Command fakeswitch serves an emulated GS1200-5 v3 on localhost.
//
// It is here so the provider can be exercised end to end — real OpenTofu, real
// plan and apply — without a switch on the desk and without the risk of
// reconfiguring one that is carrying traffic.
//
//	go run ./cmd/fakeswitch -addr 127.0.0.1:8099 -password secret
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/FernschreiberDev/terraform-provider-schaltwerk/internal/fakeswitch"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	password := flag.String("password", "secret", "web-interface password to accept")
	flag.Parse()

	device := fakeswitch.New(*password)
	log.Printf("fake GS1200-5v3 on http://%s (password %q)", *addr, *password)
	log.Fatal(http.ListenAndServe(*addr, device.Handler()))
}
