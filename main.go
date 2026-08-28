// terraform-provider-gs1200 manages Zyxel GS1200-5 v3 switches from Terraform
// and OpenTofu.
//
// The switch has no API. Everything this provider knows was derived from the
// firmware's own JavaScript and checked against the hardware.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/FernschreiberDev/terraform-provider-gs1200/internal/provider"
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
		Address: "registry.terraform.io/fernschreiberdev/gs1200",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
