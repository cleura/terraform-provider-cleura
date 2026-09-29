# A shoot is imported by its id, <project_id>/<name>, or by its name alone.
# cloud, region, and project_id come from the provider configuration, so
# configure the provider for the project that owns the cluster before importing.
terraform import cleura_gardener_shoot.example 8a22c50af68e45c6b4dd7722cce8f93a/my-cluster-name
terraform import cleura_gardener_shoot.example my-cluster-name
