mock_provider "k3s" {}

variables {
  bootstrap_host = "192.0.2.10:22"
  bootstrap_pin  = "SHA256:bootstrap"
}

run "plan_bootstrap" {
  command = plan

  assert {
    condition     = k3s_node.bootstrap.role == "bootstrap"
    error_message = "The seed must have an explicit bootstrap role."
  }
}
