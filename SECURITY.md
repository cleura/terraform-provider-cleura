# Security Policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/cleura/terraform-provider-cleura/security/advisories/new)
(the **Security** tab → *Report a vulnerability*). If you cannot use that, email
the maintainers instead of filing a public issue.

Include, as far as you can: the affected provider version and Terraform/OpenTofu
version (`terraform version`), a minimal configuration to reproduce, and the
impact you observed. We aim to acknowledge reports promptly and will keep you
updated as we investigate and fix.

## Handling credentials

The provider authenticates to the Cleura API with a username and API token. It
reads them, in order, from the provider configuration, the `CLEURA_API_USERNAME` /
`CLEURA_API_TOKEN` environment variables, or the cleura CLI (`cleura login`). The
`token` attribute is marked sensitive and the provider does not write it to its
logs. Note, however, that **Terraform state and `TF_LOG` output can contain
sensitive values** — protect them accordingly.

Cleura API tokens are short-lived. If you believe a token has been exposed, revoke
it (`cleura logout`, or via the Cleura Control Panel) and obtain a new one.

## Supported versions

This provider is in early (`0.x`) development; only the latest release receives
security fixes.
