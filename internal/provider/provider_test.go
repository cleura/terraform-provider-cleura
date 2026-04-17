package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

var (
	testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
		"cleura": providerserver.NewProtocol6WithError(New("testing")()),
	}
)

func testAccPreCheck(t *testing.T) {

	if os.Getenv("CLEURA_API_TOKEN") == "" || os.Getenv("CLEURA_API_USERNAME") == "" || os.Getenv("CLEURA_API_URL") == "" {
		t.Fatal("CLEURA_API_TOKEN, CLEURA_API_USERNAME and CLEURA_API_URL must be set for acceptance tests")
	}
}
