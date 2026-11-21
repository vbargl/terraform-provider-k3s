package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rs "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type nodeResource struct{}
type node struct {
	Name       types.String `tfsdk:"name"`
	Role       types.String `tfsdk:"role"`
	Host       types.String `tfsdk:"host"`
	User       types.String `tfsdk:"ssh_user"`
	Pin        types.String `tfsdk:"host_key_sha256"`
	Anchor     types.String `tfsdk:"anchor_host"`
	AnchorPin  types.String `tfsdk:"anchor_key_sha256"`
	Endpoint   types.String `tfsdk:"registration_url"`
	ClusterUID types.String `tfsdk:"cluster_uid"`
	ID         types.String `tfsdk:"id"`
	Ready      types.Bool   `tfsdk:"ready"`
	Identity   types.String `tfsdk:"ssh_identity_sha256"`
	Timeout    types.Int64  `tfsdk:"readiness_timeout_seconds"`
	Revision   types.Int64  `tfsdk:"reconcile_revision"`
}

func (nodeResource) Metadata(_ context.Context, req resource.MetadataRequest, r *resource.MetadataResponse) {
	r.TypeName = req.ProviderTypeName + "_node"
}
func (nodeResource) Schema(_ context.Context, _ resource.SchemaRequest, r *resource.SchemaResponse) {
	attrs := map[string]rs.Attribute{}
	for key, description := range map[string]string{
		"name": "Kubernetes node name.", "role": "bootstrap, server, or agent.",
		"host": "SSH host and port.", "ssh_user": "Account authorized for the host helper.",
		"host_key_sha256":     "Pinned OpenSSH SHA256 fingerprint of the Ed25519 host key.",
		"anchor_host":         "SSH address of a trusted, ready server for joins and membership operations.",
		"anchor_key_sha256":   "Pinned host fingerprint for the anchor.",
		"registration_url":    "Private HTTPS registration URL. Required for joining nodes.",
		"ssh_identity_sha256": "Optional SHA256 fingerprint of the client key to select from the SSH agent.",
		"cluster_uid":         "Expected kube-system namespace UID. Set from the bootstrap resource for joining nodes.",
	} {
		required := key == "name" || key == "role" || key == "host" || key == "ssh_user" || key == "host_key_sha256"
		attrs[key] = rs.StringAttribute{Description: description, Required: required, Optional: !required}
	}
	// Bootstrap computes the observed cluster identity. Joins require the same value as input.
	a := attrs["cluster_uid"].(rs.StringAttribute)
	a.Computed = true
	a.PlanModifiers = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs["cluster_uid"] = a
	attrs["id"] = rs.StringAttribute{Computed: true, Description: "Kubernetes Node UID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	attrs["reconcile_revision"] = rs.Int64Attribute{Optional: true, Description: "Change this number to explicitly retry or restart a stopped node service."}
	attrs["readiness_timeout_seconds"] = rs.Int64Attribute{Optional: true, Description: "Readiness deadline in seconds, from 30 to 3600. Defaults to 300."}
	attrs["ready"] = rs.BoolAttribute{Computed: true}
	r.Schema = rs.Schema{Attributes: attrs}
}

func (nodeResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, r *resource.ValidateConfigResponse) {
	var n node
	r.Diagnostics.Append(req.Config.Get(ctx, &n)...)
	if !r.Diagnostics.HasError() {
		r.Diagnostics.Append(validateNode(n, true)...)
	}
}

func (nodeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, r *resource.ModifyPlanResponse) {
	var desired, prior, config node
	if req.Plan.Raw.IsNull() {
		if !req.State.Raw.IsNull() {
			r.Diagnostics.Append(req.State.Get(ctx, &prior)...)
			if !r.Diagnostics.HasError() && prior.Role.ValueString() != "agent" {
				r.Diagnostics.AddError("K3s control-plane retirement is manual", "Follow the documented quorum and datastore maintenance procedure before removing the Terraform binding.")
			}
		}
		return
	}
	r.Diagnostics.Append(req.Plan.Get(ctx, &desired)...)
	r.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if r.Diagnostics.HasError() {
		return
	}
	r.Diagnostics.Append(validateNode(desired, false)...)
	r.Diagnostics.Append(validateNode(config, true)...)
	if req.State.Raw.IsNull() || r.Diagnostics.HasError() {
		return
	}
	r.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if !r.Diagnostics.HasError() {
		r.Diagnostics.Append(immutableChanges(prior, desired)...)
	}
}

func immutableChanges(prior, desired node) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	for _, field := range []struct {
		name          string
		before, after types.String
	}{
		{"name", prior.Name, desired.Name},
		{"role", prior.Role, desired.Role},
		{"registration_url", prior.Endpoint, desired.Endpoint},
		{"cluster_uid", prior.ClusterUID, desired.ClusterUID},
	} {
		if !field.after.IsUnknown() && !field.after.Equal(field.before) {
			diagnostics.AddError("K3s enrollment cannot be changed in place", field.name+" is part of the recorded node identity. Enroll a separate node or follow the documented retirement procedure.")
		}
	}
	return diagnostics
}

func (n node) readinessTimeout() time.Duration {
	if n.Timeout.IsNull() || n.Timeout.IsUnknown() {
		return 5 * time.Minute
	}
	return time.Duration(n.Timeout.ValueInt64()) * time.Second
}

func (nodeResource) Create(ctx context.Context, req resource.CreateRequest, r *resource.CreateResponse) {
	var n node
	r.Diagnostics.Append(req.Plan.Get(ctx, &n)...)
	if r.Diagnostics.HasError() {
		return
	}
	result, err := reconcile(ctx, n)
	if err != nil {
		r.Diagnostics.AddError("K3s reconciliation failed", err.Error())
		return
	}
	n.ID, n.ClusterUID, n.Ready = types.StringValue(result.UID), types.StringValue(result.ClusterUID), types.BoolValue(result.Ready)
	r.Diagnostics.Append(r.State.Set(ctx, n)...)
}
func (nodeResource) Read(ctx context.Context, req resource.ReadRequest, r *resource.ReadResponse) {
	var n node
	r.Diagnostics.Append(req.State.Get(ctx, &n)...)
	if r.Diagnostics.HasError() {
		return
	}
	result, err := observe(ctx, n)
	if err != nil {
		r.Diagnostics.AddWarning("K3s membership could not be verified", err.Error())
		n.Ready = types.BoolValue(false)
	} else {
		n.Ready = types.BoolValue(result.Ready && result.UID == n.ID.ValueString())
		switch {
		case result.UID == "":
			r.Diagnostics.AddWarning("K3s node is missing", "The cluster no longer reports this node. Its recorded identity is retained.")
		case result.UID != n.ID.ValueString():
			r.Diagnostics.AddWarning("K3s node identity changed", "The cluster reports a different UID. The provider retained the original identity.")
		case !result.Ready:
			r.Diagnostics.AddWarning("K3s node is not ready", "Inspect the recorded node and its K3s service before reconciling.")
		}
	}
	r.Diagnostics.Append(r.State.Set(ctx, n)...)
}
func (nodeResource) Update(ctx context.Context, req resource.UpdateRequest, r *resource.UpdateResponse) {
	var n, plan node
	r.Diagnostics.Append(req.State.Get(ctx, &n)...)
	r.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if r.Diagnostics.HasError() {
		return
	}
	r.Diagnostics.Append(immutableChanges(n, plan)...)
	if r.Diagnostics.HasError() {
		return
	}
	plan.ID = n.ID
	if plan.ClusterUID.IsUnknown() || plan.ClusterUID.IsNull() {
		plan.ClusterUID = n.ClusterUID
	}
	result, err := reconcile(ctx, plan)
	if err != nil {
		r.Diagnostics.AddError("K3s reconciliation failed", err.Error())
		return
	}
	plan.ID = n.ID
	plan.ClusterUID = n.ClusterUID
	plan.Ready = types.BoolValue(result.Ready)
	r.Diagnostics.Append(r.State.Set(ctx, plan)...)
}
func (nodeResource) Delete(ctx context.Context, req resource.DeleteRequest, r *resource.DeleteResponse) {
	var n node
	r.Diagnostics.Append(req.State.Get(ctx, &n)...)
	if r.Diagnostics.HasError() {
		return
	}
	if err := remove(ctx, n); err != nil {
		r.Diagnostics.AddError("K3s removal stopped", err.Error())
	}
}
