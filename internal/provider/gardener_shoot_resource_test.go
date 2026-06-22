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
	tfVars["kubernetes_version"] = config.StringVariable(os.Getenv("CLEURA_TEST_KUBERNETES_VERSION"))
	tfVars["image_version"] = config.StringVariable(os.Getenv("CLEURA_TEST_IMAGE_VERSION"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,

		Steps: []resource.TestStep{
			{
				ConfigDirectory: config.TestStepDirectory(),
				ConfigVariables: tfVars,
				Check: resource.ComposeAggregateTestCheckFunc(

					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.#", "1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.name", "wg1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.machine.image_name", "gardenlinux"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.machine.type", "b.2c4gb"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.volume_size", "50Gi"),

					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "enable_ha_control_plane"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "maintenance.auto_update.kubernetes_version"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "maintenance.auto_update.machine_image_version"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "maintenance.time_window.begin"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "maintenance.time_window.end"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "shoot_provider.infrastructure_config.workers_network_cidr"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "shoot_provider.workers.0.max_surge"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "shoot_provider.workers.0.max_unavailable"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "shoot_provider.workers.0.maximum"),
					resource.TestCheckResourceAttrSet("cleura_gardener_shoot.test", "shoot_provider.workers.0.minimum"),

					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.annotations.%", "0"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.labels.%", "0"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.taints.%", "0"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.zones.#", "1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.zones.0", "nova"),
				),
			},
			{
				ConfigDirectory: config.TestStepDirectory(),
				ConfigVariables: tfVars,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "allowed_cidrs.#", "2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "allowed_cidrs.0", "192.168.0.0/16"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "allowed_cidrs.1", "10.0.0.0/8"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "enable_ha_control_plane", "true"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "maintenance.auto_update.kubernetes_version", "false"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "maintenance.auto_update.machine_image_version", "true"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "maintenance.time_window.begin", "140000+0000"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "maintenance.time_window.end", "150000+0000"),

					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.#", "2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.name", "wg1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.1.name", "wg2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.max_surge", "1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.maximum", "2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.minimum", "2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.annotations.#", "2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.annotations.0.key", "annotation1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.annotations.0.value", "annotationvalue1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.annotations.1.key", "annotation2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.annotations.1.value", "annotationvalue2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.labels.#", "1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.labels.0.key", "label1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.labels.0.value", "labelvalue1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.taints.#", "1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.taints.0.key", "taint1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.taints.0.value", "taintvalue1"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot.test", "shoot_provider.workers.0.taints.0.effect", "NoExecute"),
				),
			},
		},
	})
}
