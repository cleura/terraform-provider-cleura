# Look up an OpenStack project ID by name within the provider's region.
# Handy for discovering the project_id to set on the provider configuration.
data "cleura_project" "example" {
  name = "my-project"
}

output "project_id" {
  value = data.cleura_project.example.id
}
