package provider

import (
	"encoding/base64"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validPin(value string) bool {
	encoded, present := strings.CutPrefix(value, "SHA256:")
	if !present {
		return false
	}
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	return err == nil && len(decoded) == 32 && len(encoded) == 43
}

func validAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	number, parseErr := strconv.ParseUint(port, 10, 16)
	return err == nil && parseErr == nil && number > 0 && host != "" && !strings.ContainsAny(host, " /@?#\t\n\r")
}

func validEndpoint(value string) bool {
	if !strings.HasPrefix(value, "https://") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		!strings.HasSuffix(u.Host, ":") && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && !strings.Contains(value, "#") &&
		(u.Port() == "" || validAddress(u.Host))
}

func validateNode(n node, config bool) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	check := func(name string, value types.String, valid func(string) bool, message string) {
		if !value.IsNull() && !value.IsUnknown() && !valid(value.ValueString()) {
			diagnostics.AddAttributeError(path.Root(name), "Invalid K3s node configuration", message)
		}
	}
	check("name", n.Name, dnsLabel.MatchString, "Use a lowercase DNS label.")
	check("role", n.Role, func(s string) bool { return s == "bootstrap" || s == "server" || s == "agent" }, "Use bootstrap, server, or agent.")
	for name, v := range map[string]types.String{"host": n.Host, "anchor_host": n.Anchor} {
		check(name, v, validAddress, "Use a host and numeric port, with brackets around an IPv6 address.")
	}
	for name, v := range map[string]types.String{"host_key_sha256": n.Pin, "anchor_key_sha256": n.AnchorPin, "ssh_identity_sha256": n.Identity} {
		check(name, v, validPin, "Use the unpadded OpenSSH SHA256 fingerprint.")
	}
	check("registration_url", n.Endpoint, validEndpoint, "Use a private HTTPS origin without a path, user information, query or fragment.")
	check("ssh_user", n.User, func(s string) bool { return strings.TrimSpace(s) != "" }, "Provide an SSH account.")
	if !n.Timeout.IsNull() && !n.Timeout.IsUnknown() && (n.Timeout.ValueInt64() < 30 || n.Timeout.ValueInt64() > 3600) {
		diagnostics.AddAttributeError(path.Root("readiness_timeout_seconds"), "Invalid readiness deadline", "Use a value from 30 through 3600 seconds.")
	}
	fields := map[string]types.String{"anchor_host": n.Anchor, "anchor_key_sha256": n.AnchorPin, "registration_url": n.Endpoint}
	switch n.Role.ValueString() {
	case "bootstrap":
		if config {
			fields["cluster_uid"] = n.ClusterUID
		}
		for name, v := range fields {
			if !v.IsNull() && (config || !v.IsUnknown()) {
				diagnostics.AddAttributeError(path.Root(name), "Bootstrap field must be omitted", "Only joining nodes configure this field.")
			}
		}
	case "server", "agent":
		fields["cluster_uid"] = n.ClusterUID
		for name, v := range fields {
			if v.IsNull() || (!v.IsUnknown() && v.ValueString() == "") {
				diagnostics.AddAttributeError(path.Root(name), "Missing join field", "Joining nodes require this value.")
			}
		}
	}
	return diagnostics
}
