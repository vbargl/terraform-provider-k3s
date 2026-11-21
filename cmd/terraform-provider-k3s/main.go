package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/vbargl/terraform-provider-k3s/internal/provider"
)

var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/vbargl/k3s",
		Debug:   false,
	})
	if err != nil {
		log.Fatal(err)
	}
}
