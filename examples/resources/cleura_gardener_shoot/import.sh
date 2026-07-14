# A shoot is imported by its name only. cloud, region, and project_id come from
# the provider configuration, so configure the provider for the project that
# owns the cluster before importing.
terraform import cleura_gardener_shoot.example my-cluster-name
