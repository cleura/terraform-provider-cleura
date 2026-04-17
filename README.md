# Terraform Provider Cleura

> [!CAUTION]
> This provider is in early stages of development and has very limited amount of testing. It is not advised to use it in a production environment!

A Terraform/OpenTofu provider for managing resources on Cleura Cloud regions. It supports both Public and Compliant cloud.

The provider datamodels and scaffolding is being generated from our OpenAPI spec using the [Terraform Provider Code Generation](https://github.com/hashicorp/terraform-plugin-codegen-openapi) tool, and the client is generated using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen). Since oapi-codegen currently does not support OAS 3.1, it needs to be downgraded to OAS 3.0, which is done using a pythin plugin called 

## Usage

TBD

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

TBD
