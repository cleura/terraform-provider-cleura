# Terraform Provider for Cleura

This is a Terraform / OpenTofu provider for [Cleura Cloud](https://cleura.com/).
It allows you to

- treat Cleura's managed services (Gardener, OpenStack projects and users) as code, and
- handle the underlying infrastructure via a suitable OpenStack provider.

The Terraform Provider is published on the [Terraform Registry](https://registry.terraform.io/providers/cleura/cleura), and works against both the Public and the Compliant cloud.

> **Warning**
>
> This provider is in early (`0.x`) development.
> The Gardener cluster surface has been tested against live Cleura environments, but coverage is still limited, and the API may change between minor versions.
> Production use is not yet recommended.

We build this in the open and **greatly value your testing and feedback**.
Trying the provider against your own Cleura environment is genuinely helpful at this stage.
If you hit a bug, see unexpected behavior, or have an improvement in mind, please [open an issue](https://github.com/cleura/terraform-provider-cleura/issues).
Real-world reports directly shape what we build next.

The following is a list of what is **currently supported:**

- `cleura_gardener_shoot:` Gardener-based Kubernetes clusters (worker groups, maintenance windows, hibernation schedules, allowed login CIDRs, and Calico/Cilium networking);
  when you create a shoot, the project is automatically prepared for Gardener.
- `cleura_gardener_shoot_kubeconfig:` short-lived admin kubeconfigs.
- `cleura_openstack_project:` OpenStack projects (name, description, enabled state).
- `cleura_openstack_user:` OpenStack users, with a write-only or a stored password.
- `cleura_openstack_role_assignment:` a user's roles on a project (see the [OpenStack projects, users and domains](docs/guides/openstack-identity.md) guide).
- `cleura_openstack_project`, `cleura_openstack_user:` (data sources) look up projects and users;
  the older `cleura_project` data source is deprecated in favor of `cleura_openstack_project`.

The provider's data models and scaffolding are generated from the Cleura OpenAPI spec using the [Terraform Provider Code Generation](https://github.com/hashicorp/terraform-plugin-codegen-openapi) tools (see [Generate](#generate)).
The API client is not generated here; the provider consumes the shared [`cleura-client-go`](https://github.com/cleura/cleura-client-go) module.

## Usage

Configure the provider and create a Gardener Kubernetes (shoot) cluster:

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
  kubernetes_version = "1.35.8"

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
          image_version = "1877.24.0"
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

The full schema for every resource and data source, including optional worker labels/annotations/taints, hibernation schedules, and maintenance windows, is documented under [`docs/`](./docs) and on the Terraform Registry.

## Authentication

The simplest setup is via the **cleura CLI.**
More specifically, run `cleura login` once, and the provider will use those credentials automatically.
For the `provider` block, you need only the values for `cloud`, `region`, and `project_id` (the default for Gardener resources).
You don't have to manage any tokens in the configuration or any environment variables.

Credentials resolve in precedence order: the first tier that provides a value wins, per value.
The Cleura CLI is the automatic fallback, so explicit configuration and environment variables always override it:

1. **Provider configuration:** `username` / `token` attributes. Highest precedence, but avoid committing secrets; prefer the CLI or environment.
2. **Environment variables:** `CLEURA_API_USERNAME` / `CLEURA_API_TOKEN` (and `CLEURA_API_URL`, for private clouds).
   Use these to override the CLI, e.g., in CI:
   ```sh
   export CLEURA_API_USERNAME="your-username"
   export CLEURA_API_TOKEN="your-token"
   ```
3. **Cleura CLI:** the default.
   If the [`cleura` CLI](https://github.com/cleura/cleura-cli) is installed and you are logged in (`cleura login`), the provider uses its credentials automatically with no configuration.
   Pin a specific CLI profile with the `profile` attribute, or disable the fallback entirely with `use_cli = false`.
   Cleura tokens are short-lived: if a plan fails with an authentication error, re-run `cleura login`.

Only **credentials** come from the `cleura` CLI.
Values for `region` and `project_id` are never read from it.
The value for `region` comes from the provider configuration (or `CLEURA_REGION`).
The value for `project_id` comes from the resource, the provider, or the `CLEURA_PROJECT_ID`; where infrastructure lives should be independent of the operator's CLI profile.

Credentials are read once, when the provider is configured.
A token expiring during a long apply fails at the affected resource call (try again after `cleura login`).
The CLI is executed with the `CLEURA_*` credential variables stripped from its environment.
That means the CLI tier reflects only the CLI's stored state; environment credentials participate as their own (higher) tier.

> **Warning**
> 
> The token grants API access to your Cleura account, so treat it like a password and never commit it to version control.
> `token` is marked sensitive in state, but environment variables are safer.

## Build

You can build and test the provider locally.
Use the following sub-sections to build and run the provider with local changes.

### Running locally from a binary

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

Within the repository root, run:

```sh
go install .
```

From then on, you can run Terraform with the locally installed provider.
If you want to install the provider from the registry, remove the `dev_overrides`.

### Running locally with VSCode debugger

To run the provider in debug mode within VSCode, create a new file `.vscode/launch.json` in the root of the repository, and initialize the `CLEURA_API_USERNAME` and `CLEURA_API_TOKEN` variables.
This is necessary: the Terraform provider won't source environment variables set in the current shell session:

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

In the dropdown menu of the *Run and Debug* tab, you should now see an option named *Debug Terraform Provider*.
Select it and press the play button to start the debug session.

This should open the *Debug console* and display an environment variable containing the connection string for this instance.
Copy this variable to your shell and then run `terraform plan`:

```sh
TF_REATTACH_PROVIDERS='{"registry.terraform.io/cleura/cleura":{"Protocol":"grpc","ProtocolVersion":6,"Pid":1181,"Test":true,"Addr":{"Network":"unix","String":"/var/folders/kg/lqlc92tx6cn2w4gm9jjst__m0000gn/T/plugin2941296319"}}}' terraform plan
```

After updating the code, restart the debug session; this generates a new connection string.
When changing the environment variables in `launch.json`, **you will need to stop** the debug session to reload any environment variable changes.

> **Note:**
> 
> If you can't see the "Debug Terraform Provider" in the debug tab, ensure you have opened the root of the repository as the workspace root.

### Generate data models

To generate all data models from the API, ensure you have the following installed on your computer:

* Terraform Provider Code Generation, OpenAPI: `go install github.com/hashicorp/terraform-plugin-codegen-openapi/cmd/tfplugingen-openapi@latest`
* Terraform Provider Code Generation, Framework: `go install github.com/hashicorp/terraform-plugin-codegen-framework/cmd/tfplugingen-framework@latest`

> **Note:**
> 
> You need to have your Go bin path configured in your $PATH to be able to run the installed Go binaries.

Generate the provider data models using the shell script:

```sh
./generate.sh
```

If you made changes to the OpenAPI spec, they have now been applied.

Documentation under `docs/` is generated separately (it is not part of `generate.sh`):

```sh
make docs
```

Every page is rendered from its template in `templates/`, with attribute reference sections generated from the schema.

## Testing

Run the unit tests (fast, no cloud credentials required):

```sh
make test
```

Acceptance tests create real clusters and therefore require credentials and `TF_ACC=1` (see the variables documented in the `testacc` target):

```sh
make testacc
```
