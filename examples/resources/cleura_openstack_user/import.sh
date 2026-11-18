# A user is imported by its OpenStack user ID (32 hex characters). Find it with
# `cleura openstack user list` or the cleura_openstack_user data source. The
# password cannot be read back, so the first apply after the import sends it:
# with password, it is stored in state; with password_wo, it is sent because
# password_wo_version goes from unset to its value, and later only when that
# version changes.
terraform import cleura_openstack_user.ci 6f1c0e6f2b1d4c0aa1b2c3d4e5f60718
