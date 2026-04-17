package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests require 60-minute timeout for shoot creation and reconciliation.
// Run with: go test -timeout 60m -run TestAccShootResource
// Or use: make testacc

func TestAccShootResource(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests require TF_ACC=1")
	}
	tfVars := make(map[string]config.Variable)
	tfVars["openstack_project_id"] = config.StringVariable(os.Getenv("CLEURA_TEST_OPENSTACK_PROJECT_ID"))
	tfVars["openstack_region_tag"] = config.StringVariable(os.Getenv("CLEURA_TEST_OPENSTACK_REGION_TAG"))
	tfVars["gardener_region_tag"] = config.StringVariable(os.Getenv("CLEURA_TEST_GARDENER_REGION_TAG"))
	tfVars["kubernetes_version"] = config.StringVariable(os.Getenv("CLEURA_TEST_KUBERNETES_VERSION"))
	tfVars["image_version"] = config.StringVariable(os.Getenv("CLEURA_TEST_IMAGE_VERSION"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) }, // check username and token are defined
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				ConfigDirectory: config.TestStepDirectory(),
				ConfigVariables: tfVars,
				Check: resource.ComposeAggregateTestCheckFunc(

					// Verify number of worker groups
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.#", "1"),
					// Verify Kubernetes version
					// resource.TestCheckResourceAttr("cleura_shoot.test", "kubernetes_version", )),
					// Verify first worker group in list
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.name", "wg1"),
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.machine.image_name", "gardenlinux"),
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.machine.type", "b.2c4gb"),
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.volume_size", "50Gi"),

					// Verify dynamic values have any value set in the state.
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "enable_ha_control_plane"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "maintenance.auto_update.kubernetes_version"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "maintenance.auto_update.machine_image_version"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "maintenance.time_window.begin"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "maintenance.time_window.end"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "shoot_provider.infrastructure_config.workers_network_cidr"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "shoot_provider.workers.0.max_surge"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "shoot_provider.workers.0.max_unavailable"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "shoot_provider.workers.0.maximum"),
					resource.TestCheckResourceAttrSet("cleura_shoot.test", "shoot_provider.workers.0.minimum"),

					// Verify annotations, labels, taints and zones are empty.
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.annotations.%", "0"),
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.labels.%", "0"),
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.taints.%", "0"),
					// Verify zones are set.
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.zones.#", "1"),
					resource.TestCheckResourceAttr("cleura_shoot.test", "shoot_provider.workers.0.zones.0", "nova"),
				),
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}
