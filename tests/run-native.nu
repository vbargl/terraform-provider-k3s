#!/usr/bin/env -S nu --no-config-file

def main [] {
    let root = ($env.FILE_PWD | path dirname)
    let terraform = ($env.TERRAFORM_BIN? | default terraform)
    let work = (^mktemp -d | str trim)
    try {
        cd $root
        let os = (^go env GOOS | str trim)
        let arch = (^go env GOARCH | str trim)
        let mirror = ($work | path join mirror registry.terraform.io vbargl k3s "0.0.1" $"($os)_($arch)")
        mkdir ($work | path join tests) $mirror
        ^cp ...(glob ($env.FILE_PWD | path join fixtures schema "*.tf")) $work
        ^cp ...(glob ($env.FILE_PWD | path join "*.tftest.hcl")) ($work | path join tests)
        with-env {CGO_ENABLED: "0"} {
            ^go build -o ($mirror | path join terraform-provider-k3s_v0.0.1) ./cmd/terraform-provider-k3s
        }
        let rc = ($work | path join terraform.rc)
        $"provider_installation {
  filesystem_mirror {
    path = (($work | path join mirror) | to json --raw)
  }
}
" | save --raw $rc
        $env.TF_CLI_CONFIG_FILE = $rc
        ^$terraform $"-chdir=($work)" init -no-color
        ^$terraform $"-chdir=($work)" test -no-color
        $env.TF_VAR_bootstrap_host = "192.0.2.10:22"
        $env.TF_VAR_bootstrap_pin = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
        for scenario in [
            {env: {TF_VAR_role: "primary"}, expected: "Use bootstrap, server, or agent."}
            {env: {TF_VAR_cluster_uid: "existing-cluster"}, expected: "Bootstrap field must be omitted"}
            {env: {TF_VAR_bootstrap_pin: "SHA256:bad"}, expected: "Use the unpadded OpenSSH SHA256 fingerprint."}
        ] {
            let result = (with-env $scenario.env {
                ^$terraform $"-chdir=($work)" plan -no-color | complete
            })
            if $result.exit_code == 0 or not ($result.stderr | str contains $scenario.expected) {
                error make {msg: $"Expected Terraform plan to reject invalid configuration: ($scenario.expected)"}
            }
        }
        print "Invalid configuration plans were rejected"
    } catch {|err|
        rm -rf $work
        error make {msg: $err.msg}
    }
    rm -rf $work
}
