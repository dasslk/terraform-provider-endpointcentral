package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/provider"
)

// version is set by goreleaser at build time.
var version = "dev"

// Format examples, then generate docs/ from the schema and examples/.
//go:generate tofu fmt -recursive ./examples/
//go:generate go tool tfplugindocs generate --provider-name endpointcentral

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.opentofu.org/dasslk/endpointcentral",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
