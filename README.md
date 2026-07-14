# Terraform Provider Cleura

A Terraform or OpenTofu provider for [Cleura Cloud](https://cleura.com/) — manage Cleura's managed
services (managed Kubernetes today) as code, alongside the OpenStack provider that handles the
underlying infrastructure. Published on the
[Terraform Registry](https://registry.terraform.io/providers/cleura/cleura); works against both
the Public and Compliant clouds.

> [!WARNING]
> This provider is in early (`0.x`) development. The Gardener cluster surface has been tested
> against live Cleura environments, but coverage is still limited, the API may change between
> minor versions, and production use is not yet recommended.

We build this in the open and **greatly value your testing and feedback** — exercising the
provider against your own Cleura environment is genuinely helpful at this stage. If you hit a bug,
see unexpected behaviour, or have an improvement in mind, please
[open an issue](https://github.com/cleura/terraform-provider-cleura/issues) on the provider
repository. Real-world reports directly shape what we build next.

**Currently supported:**

- `cleura_gardener_shoot` — Gardener-based Kubernetes clusters (worker groups, maintenance
  windows, hibernation schedules, allowed login CIDRs, and Calico/Cilium networking)
- `cleura_gardener_shoot_kubeconfig` — short-lived admin kubeconfigs
- `cleura_project` (data source) — look up Cleura projects

The provider's datamodels and scaffolding are generated from the Cleura OpenAPI spec using the
[Terraform Provider Code Generation](https://github.com/hashicorp/terraform-plugin-codegen-openapi)
tools (see [Generate](#generate)). The API client is not generated here — the provider consumes the
shared [`cleura-client-go`](https://github.com/cleura/cleura-client-go) module.

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

The full schema for every resource and data source — including optional
worker labels/annotations/taints, hibernation schedules, and maintenance
windows — is documented under [`docs/`](./docs) and on the Terraform Registry.

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

> [!WARNING]
> The token grants API access to your Cleura account — treat it like a password. Don't commit it
> to version control. `token` is marked sensitive in state, but environment variables are safer.

## Build

The provider can be built and tested locally. Use the sections in this chapter to build and run the provider with local changes.

### Running locally from binary

To build and run the provider locally, you will need a `~/.terraformrc` with development overrides:

```hcl
provider_installation {

  dev_overrides {
      # Absolute path to your Go bin directory (run `go env GOPATH` and append /bin).
      # Terraform does not expand $GOPATH, ~, or environment variables here.
      "registry.terraform.io/cleura/cleura" = "/Users/you/go/bin"
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

Now you can run Terraform with the locally installed provider as normal. Don't forget to remove the `dev_overrides` if you want to install the provider from the registry.

### Running locally with VSCode debugger

To run the provider in debug mode within VSCode, create a new file `.vscode/launch.json` in the root of the repository, fill in the `CLEURA_API_USERNAME` and `CLEURA_API_TOKEN`. This is because the terraform provider WILL NOT source environment variables set in the current shell session:

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

* Terraform Provider Code Generation, OpenAPI - `go install github.com/hashicorp/terraform-plugin-codegen-openapi/cmd/tfplugingen-openapi@latest`
* Terraform Provider Code Generation, Framework - `go install github.com/hashicorp/terraform-plugin-codegen-framework/cmd/tfplugingen-framework@latest`

> [!NOTE]
> You need to have your Go bin path configured in your $PATH to be able to run the Go binaries installed

Generate the provider datamodels using the shell script:

```sh
./generate.sh
```

If any changes were made to the OpenAPI spec, these changes have now been applied.

Documentation under `docs/` is generated separately (it is not part of `generate.sh`):

```sh
make docs
```

The provider index page is rendered from the template in `templates/`; resource and data-source
pages are generated from the schema.

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
