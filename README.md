# Terraform Provider Cleura

> [!CAUTION]
> This provider is in early stages of development and has very limited amount of testing. It is not advised to use it in a production environment!

A Terraform/OpenTofu provider for managing resources on Cleura Cloud regions. It supports both Public and Compliant cloud.

The provider datamodels and scaffolding is being generated from our OpenAPI spec using the [Terraform Provider Code Generation](https://github.com/hashicorp/terraform-plugin-codegen-openapi) tool, and the client is generated using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen). Since oapi-codegen currently does not support OAS 3.1, the spec is downgraded to OAS 3.0 using the [`openapi_downgrade`](https://pypi.org/project/openapi-downgrade/) Python tool (see [Generate](#generate)).

## Usage

Configure the provider and create a Gardener Kubernetes cluster ("shoot"):

```hcl
terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

provider "cleura" {
  cloud      = "public" # "public", "compliant", or a private cloud name
  region     = "Sto2"
  project_id = "your-project-id"
  # Credentials come from the cleura CLI — run `cleura login` first.
  # (Set username/token here, or the CLEURA_API_* env vars, to override.)
}

resource "cleura_gardener_shoot" "example" {
  name               = "example-cluster"
  kubernetes_version = "1.35.6"

  shoot_provider = {
    load_balancer_provider = "amphora"

    infrastructure_config = {
      floating_pool_name = "ext-net"
    }

    workers = [
      {
        name = "default"
        machine = {
          image_name    = "gardenlinux"
          image_version = "1877.19.0"
          type          = "b.2c4gb"
        }
        minimum     = 2
        maximum     = 4
        max_surge   = 1
        volume_size = "50Gi"
        zones       = ["nova"]
      },
    ]
  }
}

# Short-lived admin kubeconfig for the cluster above.
resource "cleura_gardener_shoot_kubeconfig" "example" {
  shoot_name         = cleura_gardener_shoot.example.name
  expiration_seconds = 3600
}
```

## Authentication

The simplest setup is the **cleura CLI**: run `cleura login` once and the
provider uses those credentials automatically — the provider block then needs
only `cloud`, `region`, and `project_id`. No token in configuration, no
environment variables to manage.

Credentials resolve in precedence order (the first tier that provides a value
wins, per value). The CLI is the automatic fallback, so explicit configuration
and environment variables always override it:

1. **Provider configuration** — `username` / `token` attributes. Highest
   precedence, but avoid committing secrets; prefer the CLI or environment.
2. **Environment variables** — `CLEURA_API_USERNAME` / `CLEURA_API_TOKEN`
   (and `CLEURA_API_URL` for private clouds). Use these to override the CLI —
   for example in CI:

   ```sh
   export CLEURA_API_USERNAME="your-username"
   export CLEURA_API_TOKEN="your-token"
   ```

3. **The cleura CLI** — the default. If the
   [`cleura` CLI](https://github.com/cleura/cleura-cli) is installed and logged
   in (`cleura login`), the provider uses its credentials automatically, with
   no configuration. Pin a specific CLI profile with the `profile` attribute,
   or disable the fallback entirely with `use_cli = false`. Cleura tokens are
   short-lived: if a plan fails with an authentication error, re-run
   `cleura login`.

Only **credentials** come from the CLI. `region` and `project_id` must always
be stated in the provider configuration (or their environment variables):
where infrastructure lives should never depend on the operator's CLI profile.

Credentials are read once, when the provider is configured: a token expiring
during a long apply fails at the affected resource call — re-run after
`cleura login`. The CLI is executed with the `CLEURA_*` credential variables
stripped from its environment, so the CLI tier reflects the CLI's stored
state only; environment credentials participate as their own (higher) tier.

The full schema for every resource and data source — including optional
worker labels/annotations/taints, hibernation schedules, and maintenance
windows — is documented under [`docs/`](./docs) and on the Terraform Registry.

## Build

The provider can be built and tested locally. Use the sections in this chapter to build and run the provider with local changes.

### Running locally from binary

To build and run the provider locally, you will need a `~/.terraformrc` with development overrides:

```hcl
provider_installation {

  dev_overrides {
      "registry.terraform.io/cleura/cleura" = "<path to you $GOPATH>"
  }

  # For all other providers, install them directly from their origin provider
  # registries as normal. If you omit this, Terraform will _only_ use
  # the dev_overrides block, and so no other providers will be available.
  direct {}
}
```

Within the repository root, run

```sh
go install .
```

Now you can run Terraform with the locally installed provider as normal. Don't forget to remove the `dev_overrides` if you want to install the proivder from the registry

### Running locally with VSCode debugger

To run the provider in debug mode withing VSCode, create a new file `.vscode/launch.json` in the root of the repository, fill in the `CLEURA_API_USERNAME` and `CLEURA_API_TOKEN`. This is because the terraform provider WILL NOT source environment variables set in the current shell session:

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Debug Terraform Provider",
            "type": "go",
            "request": "launch",
            "mode": "debug",
            // this assumes your workspace is the root of the repo
            "program": "${workspaceFolder}",
            "env": {
                "CLEURA_API_USERNAME": "<your username>",
                "CLEURA_API_TOKEN": "<your token>"
            },
            "args": [
                "-debug",
            ]
        }
    ]
}
```

In the "Run and Debug" tab, you should now see "Debug Terraform Provider" in the dropdown menu. Select it and press the play button to start the debug session.

This should open the "Debug console" and display an environment variable containing the connection string for this instance. Copy this variable to your shell and run terraform right after it:

```sh
TF_REATTACH_PROVIDERS='{"registry.terraform.io/cleura/cleura":{"Protocol":"grpc","ProtocolVersion":6,"Pid":1181,"Test":true,"Addr":{"Network":"unix","String":"/var/folders/kg/lqlc92tx6cn2w4gm9jjst__m0000gn/T/plugin2941296319"}}}' terraform plan
```

After updating the code, you will need to restart the debug session, which also generates a new connection string. When changing the environment variables in `launch.json`, you will need to STOP the debug session to reload any environment variable changes.

> [!NOTE]
> If you can't see the "Debug Terraform Provider" in the debug tab, ensure you have opened the root of the repository as the workspace root.

### Generate

To generate all datamodels from the API, ensure you have the following installed on your machine:

* OpenAPI downgrader - `pip install openapi_downgrade`
* Terraform Provider Code Generation - `go install github.com/hashicorp/terraform-plugin-codegen-openapi/cmd/tfplugingen-openapi@latest`
* oapi-codegen - `go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest`

> [!NOTE]
> You need to have your Go bin path configured in your $PATH to be able to run the Go binaries installed

Generate the provider and client using the shell script:

```sh
./generate.sh
```

If any changes were made to the OpenAPI spec, these changes has now been applied.

## Test

Run the unit tests (fast, no cloud credentials required):

```sh
make test
```

Acceptance tests create real clusters and therefore require credentials and
`TF_ACC=1` (see the variables documented in the `testacc` target):

```sh
make testacc
```
