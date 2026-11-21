terraform {
  required_providers {
    k3s = {
      source = "vbargl/k3s"
    }
  }
}

variable "bootstrap_host" {
  type = string
}

variable "bootstrap_pin" {
  type = string
}

resource "k3s_node" "bootstrap" {
  name            = "bootstrap"
  role            = "bootstrap"
  host            = var.bootstrap_host
  host_key_sha256 = var.bootstrap_pin
  ssh_user        = "k3s-coordinator"
}
