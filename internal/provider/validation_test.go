package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

const testPin = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func configuredNode(role string) node {
	return node{
		Name: types.StringValue("worker"),
		Role: types.StringValue(role),
		Host: types.StringValue("test.example:22"),
		User: types.StringValue("coord"),
		Pin:  types.StringValue(testPin),
	}
}

func TestValidateNode(t *testing.T) {
	bootstrap := configuredNode("bootstrap")
	agent := configuredNode("agent")
	agent.Anchor = types.StringValue("[2001:db8::1]:22")
	agent.AnchorPin = types.StringValue(testPin)
	agent.Endpoint = types.StringValue("https://[2001:db8::1]:6443")
	agent.ClusterUID = types.StringUnknown()

	if diagnostics := validateNode(bootstrap, true); diagnostics.HasError() {
		t.Fatalf("valid bootstrap: %v", diagnostics)
	}
	if diagnostics := validateNode(agent, true); diagnostics.HasError() {
		t.Fatalf("valid join with unknown cluster UID: %v", diagnostics)
	}

	for _, test := range []struct {
		name   string
		change func(*node)
	}{
		{"role", func(n *node) { n.Role = types.StringValue("primary") }},
		{"name", func(n *node) { n.Name = types.StringValue("Bad_Name") }},
		{"host port", func(n *node) { n.Host = types.StringValue("test.example") }},
		{"missing anchor", func(n *node) { n.Anchor = types.StringNull() }},
		{"malformed pin", func(n *node) { n.Pin = types.StringValue("SHA256:bad") }},
		{"uppercase URL scheme", func(n *node) { n.Endpoint = types.StringValue("HTTPS://test.example") }},
		{"URL query", func(n *node) { n.Endpoint = types.StringValue("https://test.example?") }},
		{"URL credentials", func(n *node) { n.Endpoint = types.StringValue("https://user@test.example") }},
		{"timeout", func(n *node) { n.Timeout = types.Int64Value(10) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := agent
			test.change(&changed)
			if !validateNode(changed, true).HasError() {
				t.Fatal("expected validation error")
			}
		})
	}
	bootstrap.ClusterUID = types.StringValue("explicit")
	if !validateNode(bootstrap, true).HasError() || validateNode(bootstrap, false).HasError() {
		t.Fatal("bootstrap cluster UID must be computed, not configured")
	}
}

func TestImmutableChanges(t *testing.T) {
	prior := configuredNode("agent")
	prior.ClusterUID = types.StringValue("same-cluster")
	desired := prior
	desired.Name = types.StringValue("replacement")
	if !immutableChanges(prior, desired).HasError() {
		t.Fatal("rename would destroy the old node")
	}
	desired.Name = types.StringUnknown()
	desired.Host = types.StringValue("new-address.example:22")
	if immutableChanges(prior, desired).HasError() {
		t.Fatal("unknown values and SSH address updates should be allowed")
	}
}
