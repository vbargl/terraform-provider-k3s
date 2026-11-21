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
  role            = var.role
  host            = var.bootstrap_host
  host_key_sha256 = var.bootstrap_pin
  ssh_user        = "k3s-coordinator"
  cluster_uid     = var.cluster_uid
}

variable "role" {
  type    = string
  default = "bootstrap"
}

variable "cluster_uid" {
  type    = string
  default = null
}
