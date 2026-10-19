package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/opentofu/opentofu/provider/internal/multiformprovider"
)

func main() {
	err := providerserver.Serve(context.Background(), multiformprovider.New, providerserver.ServeOpts{
		Address: "registry.opentofu.org/local/multiform",
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
