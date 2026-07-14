---
page_title: "Getting Started"
subcategory: "Guides"
description: |-
  Install the cleura CLI, log in, and create your first Gardener Kubernetes
  cluster with the Cleura Terraform/OpenTofu provider.
---

# Getting Started

This guide takes you from nothing to a running Gardener Kubernetes cluster on
Cleura Cloud. The quickest path is to authenticate once with the
[`cleura` CLI](https://github.com/cleura/cleura-cli) and let the provider reuse
those credentials — so the provider block needs only `cloud`, `region`, and
`project_id`, and no secret ever lands in your configuration or state.

## 1. Install the cleura CLI

The provider reads credentials from the CLI by default, so install it first.
Pick whichever method suits you; full details, checksum verification, and shell
completion are in the [CLI README](https://github.com/cleura/cleura-cli#install).

**Install script** (Linux/macOS) — downloads the latest release, verifies its
checksum, and installs it (to `/usr/local/bin`, using `sudo` if needed):

```shell
curl -fsSL https://raw.githubusercontent.com/cleura/cleura-cli/main/install.sh | sh
```

Override the install directory with `BINDIR=$HOME/.local/bin`, or pin a version
with `CLEURA_VERSION=v0.7.0`.

**Prebuilt binary** — download an archive for your OS/architecture from the
[latest release](https://github.com/cleura/cleura-cli/releases/latest) and put
`cleura` on your `PATH`.

**With Go** (1.25+):

```shell
go install github.com/cleura/cleura-cli/cmd/cleura@latest
```

**Homebrew** (once the tap is published):

```shell
brew install cleura/tap/cleura
```

Confirm it is on your `PATH`:

```shell
cleura --version
```

## 2. Log in

`cleura login` prompts for your username and password and, when your account
uses it, an SMS two-factor code. It stores a **short-lived API token** in the
CLI config; the provider picks that up automatically.

```shell
cleura login          # prompts for username / password (SMS 2FA supported)
cleura whoami         # confirm who you are authenticated as
```

Working across more than one account or cloud is a matter of profiles. Log in
to a named profile and reference it later from the provider's `profile`
attribute (see section 6):

```shell
cleura login --profile work    # stores credentials under the "work" profile
```

Cleura tokens expire — if a later `terraform plan`/`apply` fails with an
authentication error, just run `cleura login` again.

## 3. Configure the provider

Create `main.tf`. Pin the provider version and configure a provider block. With
the CLI logged in, the block needs only `cloud`, `region`, and `project_id` —
the username and token come from the CLI.

```terraform
terraform {
  required_providers {
    cleura = {
      source  = "cleura/cleura"
      version = "~> 0.1"
    }
  }
}

provider "cleura" {
  cloud      = "public" # "public", "compliant", or a private cloud name
  region     = "Sto2"   # case-sensitive region tag — see note below
  project_id = "your-project-id"
}
```

~> **`region` is a case-sensitive tag.** The Cleura API returns capitalized
region tags such as `Sto2`, `Fra1`, and `Kna1`, and matching is case-sensitive.
Use the tag exactly as Cleura reports it — `Sto2`, not `sto2`.

**Finding your `project_id`.** `region` and `project_id` are always taken from
the provider configuration (or the `CLEURA_REGION` / `CLEURA_PROJECT_ID`
environment variables) and are never read from the CLI. The fastest way to get
the ID is the CLI you just installed:

```shell
cleura openstack project list   # projects you can access, with their IDs
```

Or discover it from Terraform with the `cleura_project` data source, which needs
only `cloud` + `region` (not `project_id`). Add this to a scratch configuration,
run `terraform apply`, then copy the ID into the provider block above:

```terraform
data "cleura_project" "this" {
  name = "my-project"
}

output "project_id" {
  value = data.cleura_project.this.id
}
```

Do not wire the data source's `id` directly into the provider block — provider
configuration is resolved before data sources are read, so that would create a
dependency cycle. Keep `project_id` a literal (or a variable) and use the data
source only to look the value up or to verify it matches a project name.

## 4. Create your first cluster

Append a `cleura_gardener_shoot` resource and a
`cleura_gardener_shoot_kubeconfig` to fetch a short-lived admin kubeconfig. The
shoot **name must be 15 characters or fewer** (letters, digits, and hyphens).

```terraform
resource "cleura_gardener_shoot" "demo" {
  name               = "demo" # 15 characters max
  kubernetes_version = "1.35.6"

  shoot_provider = {
    load_balancer_provider = "amphora"

    infrastructure_config = {
      floating_pool_name = "ext-net"
    }

    # Worker groups are matched by name; keep their order stable in config.
    workers = [
      {
        name = "default"
        machine = {
          image_name    = "gardenlinux"
          image_version = "1877.19.0"
          type          = "b.2c4gb"
        }
        minimum     = 1
        maximum     = 3
        volume_size = "50Gi"
        zones       = ["nova"]
      },
    ]
  }
}

# A short-lived admin kubeconfig for the cluster. It is regenerated
# automatically once it reaches its renewal window.
resource "cleura_gardener_shoot_kubeconfig" "demo" {
  shoot_name         = cleura_gardener_shoot.demo.name
  expiration_seconds = 3600
}

# The kubeconfig is a credential — mark the output sensitive.
output "kubeconfig" {
  value     = cleura_gardener_shoot_kubeconfig.demo.kubeconfig
  sensitive = true
}
```

-> The `kubernetes_version`, `image_version`, machine `type`, `zones`, and
`floating_pool_name` above are illustrative. Run
`cleura gardener cloud-profile show cleuracloud` to list the Kubernetes
versions, machine images, and machine types currently offered in your cloud,
and use the values that apply to your project and region.

## 5. Run it

```shell
terraform init      # download the provider
terraform plan      # review what will be created
terraform apply     # create the cluster
```

`terraform apply` **blocks for several minutes** while Cleura reconciles the
cluster — provisioning the control plane and worker nodes takes time. When it
returns, retrieve the kubeconfig and talk to your cluster:

```shell
terraform output -raw kubeconfig > demo.kubeconfig
kubectl --kubeconfig demo.kubeconfig get nodes
```

## 6. Authentication alternatives

The CLI is only the default credential source. Credentials resolve in this
precedence order (each tier overrides the ones below it):

1. **Provider configuration** — the `username` / `token` attributes.
2. **Environment variables** — `CLEURA_API_USERNAME` / `CLEURA_API_TOKEN` (plus
   `CLEURA_API_URL` for private clouds).
3. **The cleura CLI** — the automatic fallback described above.

**Provide credentials explicitly / disable the CLI.** Set `username` and `token`
(keep the token out of source — use a variable or `CLEURA_API_TOKEN`), and set
`use_cli = false` to skip the CLI tier entirely:

```terraform
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "your-project-id"

  username = var.cleura_username # or the CLEURA_API_USERNAME env var
  token    = var.cleura_token    # or the CLEURA_API_TOKEN env var
  use_cli  = false               # never consult the cleura CLI
}
```

**Pick a specific CLI profile.** If you logged in with
`cleura login --profile work`, point the provider at it:

```terraform
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "your-project-id"
  profile    = "work" # read fallback credentials from the "work" profile
}
```

**In CI**, there is no interactive terminal, so authenticate one of two
non-interactive ways:

- **Pre-created token** — set `CLEURA_API_USERNAME` and `CLEURA_API_TOKEN` as
  masked secrets; the provider reads them directly and the CLI need not be
  installed. Mint the token shortly before the run, since Cleura tokens are
  short-lived.
- **`cleura login` in the job** — install the CLI, set `CLEURA_API_PASSWORD` as a
  masked secret, and run `cleura login -u "$CLEURA_USERNAME"` at the start of the
  job. It logs in with no prompt and no secret on the command line, minting a
  fresh token each run that the provider then picks up from the CLI. This is how
  you authenticate from a *password* rather than a pre-issued token, and it needs
  a single-factor service account (SMS two-factor cannot be completed in CI).

Either way, set the topology (`CLEURA_REGION`, `CLEURA_PROJECT_ID`, `CLEURA_CLOUD`)
as plain job variables. A pre-created-token job looks like:

```shell
export CLEURA_API_USERNAME="$CLEURA_API_USERNAME" # from a masked CI secret
export CLEURA_API_TOKEN="$CLEURA_API_TOKEN"       # from a masked CI secret
export CLEURA_REGION="Sto2"
export CLEURA_PROJECT_ID="your-project-id"

terraform init
terraform apply -auto-approve
```

The [Authentication guide](authentication.md#running-in-ci) has complete CI
recipes for both paths (a `cleura login` job and a CLI-less curl token prestep),
and ready-to-copy `cleura login` pipelines (GitHub Actions, GitLab CI, plain
shell) live in the CLI repository under
[`examples/ci/`](https://github.com/cleura/cleura-cli/tree/main/examples/ci).

~> **Treat an API token like a password** — it grants access to your Cleura
account. Never commit it. The `token` attribute is marked sensitive, and
`cleura login` and the environment variables keep it out of your configuration
and state.

## 7. Using the OpenStack provider alongside this one

This provider manages Cleura's **managed services** — Gardener Kubernetes today.
The infrastructure underneath those services — networks, routers, security
groups, floating IP pools, images, volumes, and standalone compute — lives in
OpenStack and is managed with the
[OpenStack provider](https://registry.terraform.io/providers/terraform-provider-openstack/openstack/latest).
A common setup runs both in the same configuration:

```terraform
terraform {
  required_providers {
    cleura = {
      source  = "cleura/cleura"
      version = "~> 0.1"
    }
    openstack = {
      source = "terraform-provider-openstack/openstack"
    }
  }
}

# Managed Kubernetes on Cleura.
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "your-project-id"
}

# The infrastructure underneath it.
provider "openstack" {
  # Authenticates separately from the cleura provider — e.g. via a clouds.yaml
  # / OS_* environment variables or application credentials. The cleura CLI
  # does not feed credentials to the OpenStack provider.
}
```

Rough division of labor:

- **`cleura` provider** — Gardener shoots, their worker groups, and kubeconfigs
  (the managed Kubernetes control plane and node pools).
- **OpenStack provider** — the tenant's networking, storage, images, keypairs,
  and any VMs you run outside Gardener.

The two share the same OpenStack project: the `project_id` you set on the
`cleura` provider is the same tenant the OpenStack provider authenticates
against. They authenticate independently, so configure each one's credentials as
described in its own documentation. For interactive, day-to-day OpenStack work
beyond Terraform, use
[`cleura-openstackclient`](https://github.com/cleura/cleura-openstackclient).

