package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

type k3sProvider struct{ version string }

// New returns a constructor for the K3s provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return k3sProvider{version: version} }
}

func (p k3sProvider) Metadata(_ context.Context, _ provider.MetadataRequest, r *provider.MetadataResponse) {
	r.TypeName, r.Version = "k3s", p.version
}
func (k3sProvider) Schema(_ context.Context, _ provider.SchemaRequest, r *provider.SchemaResponse) {
	r.Schema = schema.Schema{Description: "Coordinate K3s nodes on hosts managed by Console."}
}
func (k3sProvider) Configure(context.Context, provider.ConfigureRequest, *provider.ConfigureResponse) {
}
func (k3sProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }
func (k3sProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return nodeResource{} }}
}
