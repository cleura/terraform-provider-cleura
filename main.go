package main

import (
	"context"
	"flag"
	"log"

	"github.com/cleura/terraform-provider-cleura/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var (
	// These will be set by the goreleaser configuration.
	// Set to appropriate values for the compiled binary.
	version = "dev"

	// `goreleaser` can pass other info to the main package, e.g. commit hash.
	// https://goreleaser.com/cookbooks/using-main.version/
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/cleura/cleura",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)

	if err != nil {
		log.Fatal(err.Error())
	}
}
