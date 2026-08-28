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
	"strconv"
	"strings"

	"github.com/FernschreiberDev/terraform-provider-schaltwerk/internal/fakeswitch"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	password := flag.String("password", "secret", "web-interface password to accept")
	vlans := flag.String("vlans", "", "VLAN table in /vlanEntry.xml wire format; empty keeps the captured gs1200 one")
	pvids := flag.String("pvids", "", "comma-separated PVID per port, starting at port 1")
	name := flag.String("name", "", "device name; empty keeps the captured one")
	flag.Parse()

	device := fakeswitch.New(*password)
	if *name != "" {
		device.SysName = *name
	}
	if *vlans != "" && *pvids != "" {
		table := map[int]int{}
		for index, raw := range strings.Split(*pvids, ",") {
			vid, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				log.Fatalf("bad -pvids entry %q: %v", raw, err)
			}
			table[index+1] = vid
		}
		device.Seed(*vlans, table)
	}
	log.Printf("fake GS1200-5v3 on http://%s (password %q)", *addr, *password)
	log.Fatal(http.ListenAndServe(*addr, device.Handler()))
}
