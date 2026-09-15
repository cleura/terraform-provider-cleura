# A user is imported by its OpenStack user ID (32 hex characters). Find it with
# `cleura openstack user list` or the cleura_openstack_user data source. The
# password cannot be read back; set it in the configuration as usual and it is
# only re-sent when password_wo_version changes.
terraform import cleura_openstack_user.ci 6f1c0e6f2b1d4c0aa1b2c3d4e5f60718
