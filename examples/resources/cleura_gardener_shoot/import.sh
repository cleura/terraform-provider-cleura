# A shoot is imported by its id, <project_id>/<name>, or by its name alone.
# cloud and region come from the provider configuration. A bare name uses the
# provider's project_id; an id may name any project the credentials can access.
terraform import cleura_gardener_shoot.example 0f3c1e2a4b5d6e7f8091a2b3c4d5e6f7/my-cluster-name
terraform import cleura_gardener_shoot.example my-cluster-name
