# Terraform Provider Cleura

> [!WARNING]
> This provider is in early (`0.x`) development. The Gardener cluster surface has been tested
> against live Cleura environments, but coverage is still limited, the API may change between
> minor versions, and production use is not yet recommended.

A Terraform/OpenTofu provider for managing resources on Cleura Cloud, published on the
[Terraform Registry](https://registry.terraform.io/providers/cleura/cleura). It works against
both the Public and Compliant clouds.

**Currently supported:**

- `cleura_gardener_shoot` — Gardener-based Kubernetes clusters (worker groups, maintenance
  windows, hibernation schedules, allowed login CIDRs, and Calico/Cilium networking)
- `cleura_gardener_shoot_kubeconfig` — short-lived admin kubeconfigs
- `cleura_project` (data source) — look up Cleura projects

The provider's datamodels and scaffolding are generated from the Cleura OpenAPI spec using the
[Terraform Provider Code Generation](https://github.com/hashicorp/terraform-plugin-codegen-openapi)
tool, and the client is generated with [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen).
Since oapi-codegen does not yet support OAS 3.1, the spec is downgraded to OAS 3.0 using the
[`openapi_downgrade`](https://pypi.org/project/openapi-downgrade/) Python tool (see [Generate](#generate)).

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
  # Credentials are read from CLEURA_API_USERNAME and CLEURA_API_TOKEN
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

The provider authenticates to the Cleura API with a **username** and an **API token** (sent as
the `X-AUTH-LOGIN` and `X-AUTH-TOKEN` headers). You supply a token you have already created — the
provider does not create one for you. See the
[Cleura Cloud API documentation](https://rest.cleura.cloud/apidoc) for the full reference.

### Create a token

The repository includes a helper script that walks you through it — it handles cloud selection
(public/compliant) and two-factor authentication. It requires `curl` and `jq`:

```sh
./scripts/cleura_token.sh
```

It prompts for your username, password, and cloud, then returns a token.

Alternatively, call the API directly. This simple form works for accounts **without** 2FA; for the
2FA flow use the script above or see the [API docs](https://rest.cleura.cloud/apidoc):

```sh
curl -H "Content-Type: application/json" -X POST \
  -d '{"auth": {"login": "your-username", "password": "your-password"}}' \
  https://rest.cleura.cloud/auth/v1/tokens
# => { "result": "login_ok", "token": "<your-token>" }
```

Your login is the provider `username`; the returned `token` is the provider `token`. A token is
valid for the remainder of the session and can be reused across requests until it expires.

### Configure the provider

Supply the credentials via environment variables (recommended — keeps secrets out of your
configuration and state) or provider arguments:

| Credential      | Provider argument | Environment variable  |
| --------------- | ----------------- | --------------------- |
| Cleura username | `username`        | `CLEURA_API_USERNAME` |
| API token       | `token`           | `CLEURA_API_TOKEN`    |

```sh
export CLEURA_API_USERNAME="your-username"
export CLEURA_API_TOKEN="your-token"
```

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

* OpenAPI downgrader - `pip install openapi-downgrade`
* Terraform Provider Code Generation, OpenAPI - `go install github.com/hashicorp/terraform-plugin-codegen-openapi/cmd/tfplugingen-openapi@latest`
* Terraform Provider Code Generation, Framework - `go install github.com/hashicorp/terraform-plugin-codegen-framework/cmd/tfplugingen-framework@latest`
* oapi-codegen - `go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest`

> [!NOTE]
> You need to have your Go bin path configured in your $PATH to be able to run the Go binaries installed

Generate the provider and client using the shell script:

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
