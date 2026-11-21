variables {
  bootstrap_host = "192.0.2.10:22"
  bootstrap_pin  = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}

run "plan_bootstrap" {
  command = plan

  assert {
    condition     = k3s_node.bootstrap.role == "bootstrap"
    error_message = "The seed must have an explicit bootstrap role."
  }
}
