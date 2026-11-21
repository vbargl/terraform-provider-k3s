package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"golang.org/x/crypto/ssh"
)

func resourceState(t *testing.T, n node) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	var schema resource.SchemaResponse
	(nodeResource{}).Schema(ctx, resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	if diagnostics := state.Set(ctx, n); diagnostics.HasError() {
		t.Fatalf("building state: %v", diagnostics)
	}
	return state
}

func TestModifyPlanRejectsEnrollmentChangesAndServerDestroy(t *testing.T) {
	ctx := context.Background()
	prior := configuredNode("bootstrap")
	prior.ClusterUID = types.StringValue("cluster")
	state := resourceState(t, prior)
	desired := prior
	desired.Name = types.StringValue("renamed")
	plan := tfsdk.Plan{Schema: state.Schema, Raw: resourceState(t, desired).Raw}
	desired.ClusterUID = types.StringNull()
	config := tfsdk.Config{Schema: state.Schema, Raw: resourceState(t, desired).Raw}
	var modified resource.ModifyPlanResponse
	(nodeResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan, Config: config}, &modified)
	if len(modified.Diagnostics.Errors()) != 1 || modified.Diagnostics.Errors()[0].Summary() != "K3s enrollment cannot be changed in place" {
		t.Fatalf("expected enrollment diagnostic, got %v", modified.Diagnostics)
	}
	for _, role := range []string{"bootstrap", "server", "agent"} {
		n := configuredNode(role)
		stored := resourceState(t, n)
		destroy := tfsdk.Plan{Schema: stored.Schema, Raw: tftypes.NewValue(stored.Raw.Type(), nil)}
		var outcome resource.ModifyPlanResponse
		(nodeResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{State: stored, Plan: destroy}, &outcome)
		if outcome.Diagnostics.HasError() != (role != "agent") {
			t.Fatalf("unexpected destroy plan for %s: %v", role, outcome.Diagnostics)
		}
	}
}

func TestReadWarnsAndKeepsRecordedUID(t *testing.T) {
	ctx := context.Background()
	_, host := testKey(t)
	private, client := testKey(t)
	startSSHAgent(t, private)
	pin := types.StringValue(ssh.FingerprintSHA256(host.PublicKey()))
	for _, test := range []struct{ name, reply, warning string }{
		{"missing", `{"cluster_uid":"cluster"}`, "K3s node is missing"},
		{"replaced", `{"cluster_uid":"cluster","uid":"other","ready":true}`, "K3s node identity changed"},
		{"unready", `{"cluster_uid":"cluster","uid":"old","ready":false}`, "K3s node is not ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, _ := startSSHHost(t, host, client, `{"machine_id":"worker-machine","uid":"old"}`, 0)
			anchor, _ := startSSHHost(t, host, client, test.reply, 0)
			n := configuredNode("agent")
			n.Host, n.Anchor = types.StringValue(target), types.StringValue(anchor)
			n.Pin, n.AnchorPin = pin, pin
			n.ID, n.ClusterUID, n.Ready = types.StringValue("old"), types.StringValue("cluster"), types.BoolValue(true)
			state := resourceState(t, n)
			result := resource.ReadResponse{State: state}
			(nodeResource{}).Read(ctx, resource.ReadRequest{State: state}, &result)
			warnings := result.Diagnostics.Warnings()
			if result.Diagnostics.HasError() || len(warnings) != 1 || warnings[0].Summary() != test.warning {
				t.Fatalf("expected warning %q, got %v", test.warning, result.Diagnostics)
			}
			var observed node
			if diagnostics := result.State.Get(ctx, &observed); diagnostics.HasError() {
				t.Fatalf("reading refreshed state: %v", diagnostics)
			}
			if observed.Ready.ValueBool() || !observed.ID.Equal(n.ID) {
				t.Fatalf("refresh changed recorded identity or reported ready")
			}
		})
	}
}
