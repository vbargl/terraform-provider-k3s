terraform {
  required_providers {
    k3s = {
      source = "vbargl/k3s"
    }
  }
}

resource "k3s_node" "bootstrap" {
  name            = "bootstrap"
  role            = "bootstrap"
  host            = "${var.bootstrap_address}:${var.ssh_port}"
  host_key_sha256 = var.bootstrap_pin
  ssh_user        = var.ssh_user
}

resource "k3s_node" "member" {
  for_each = {
    peer   = { role = "server", host = "${var.server_address}:${var.ssh_port}", pin = var.server_pin }
    worker = { role = "agent", host = "${var.worker_address}:${var.ssh_port}", pin = var.worker_pin }
  }
  reconcile_revision = each.key == "worker" ? var.worker_revision : 0
  name               = each.key
  role               = each.value.role
  host               = each.value.host
  host_key_sha256    = each.value.pin
  ssh_user           = var.ssh_user
  anchor_host        = k3s_node.bootstrap.host
  anchor_key_sha256  = k3s_node.bootstrap.host_key_sha256
  registration_url   = "https://${var.bootstrap_address}:6443"
  cluster_uid        = k3s_node.bootstrap.cluster_uid
}

variable "bootstrap_address" {
  type = string
}

variable "server_address" {
  type = string
}

variable "worker_address" {
  type = string
}

variable "bootstrap_pin" {
  type = string
}

variable "server_pin" {
  type = string
}

variable "worker_pin" {
  type = string
}

variable "ssh_user" {
  type = string
}

variable "ssh_port" {
  type = string
}

variable "worker_revision" {
  type    = number
  default = 0
}
