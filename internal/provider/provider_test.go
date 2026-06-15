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
	if os.Getenv("CLEURA_API_TOKEN") == "" || os.Getenv("CLEURA_API_USERNAME") == "" {
		t.Fatal("CLEURA_API_TOKEN and CLEURA_API_USERNAME must be set for acceptance tests")
	}
	if os.Getenv("CLEURA_API_URL") == "" && os.Getenv("CLEURA_CLOUD") == "" {
		t.Fatal("Set CLEURA_CLOUD or CLEURA_API_URL for acceptance tests")
	}
	if os.Getenv("CLEURA_REGION") == "" {
		t.Fatal("CLEURA_REGION must be set for acceptance tests")
	}
	if os.Getenv("CLEURA_PROJECT_ID") == "" {
		t.Fatal("CLEURA_PROJECT_ID must be set for acceptance tests")
	}
	if os.Getenv("CLEURA_TEST_KUBERNETES_VERSION") == "" {
		t.Fatal("CLEURA_TEST_KUBERNETES_VERSION must be set for acceptance tests")
	}
	if os.Getenv("CLEURA_TEST_IMAGE_VERSION") == "" {
		t.Fatal("CLEURA_TEST_IMAGE_VERSION must be set for acceptance tests")
	}
}
