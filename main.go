// terraform-provider-schaltwerk manages Zyxel GS1200 switches from OpenTofu.
//
// The name is the German for switchgear — the cabinet where circuits are
// actually thrown, as opposed to the diagram of them.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/FernschreiberDev/terraform-provider-schaltwerk/internal/provider"
)

// version is stamped at build time: -ldflags "-X main.version=0.1.0".
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false,
		"run with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		// The address OpenTofu resolves in required_providers. It is not a URL
		// and nothing is fetched from it: with a filesystem mirror, this is
		// simply the key under which the binary is filed.
		Address: "registry.opentofu.org/fernschreiberdev/schaltwerk",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
