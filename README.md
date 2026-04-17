# Terraform Provider Cleura

> [!CAUTION]
> This provider is in early stages of development and has very limited amount of testing. It is not advised to use it in a production environment!

A Terraform/OpenTofu provider for managing resources on Cleura Cloud regions. It supports both Public and Compliant cloud.

The provider datamodels and scaffolding is being generated from our OpenAPI spec using the [Terraform Provider Code Generation](https://github.com/hashicorp/terraform-plugin-codegen-openapi) tool, and the client is generated using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen). Since oapi-codegen currently does not support OAS 3.1, it needs to be downgraded to OAS 3.0, which is done using a pythin plugin called 

## Usage

TBD

## Build

The provider can be built and tested locally. Use the sections in this chapter to build and run the provider with local changes.

### Running locally

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

TBD
