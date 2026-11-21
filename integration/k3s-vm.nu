#!/usr/bin/env -S nu --no-config-file
# Use only freshly provisioned disposable VMs. Obtain host pins from their consoles.

def require [condition: bool, message: string] {
    if not $condition { error make {msg: $message} }
}

def logged [command: string, log: string, ...args: string] {
    let result = (^$command ...$args | complete)
    $"($result.stdout)($result.stderr)" | save --raw $log
    $result.exit_code
}

def scan [files: list<string>, secrets: list<string>] {
    for file in $files {
        let contents = if ($file | str ends-with ".tfplan") { ^unzip -p $file | into binary } else { open --raw $file | into binary }
        for secret in $secrets {
            require (($contents | bytes index-of ($secret | into binary)) == -1) $"Test credential leaked into ($file)"
        }
    }
}

def exercise [scratch: string] {
    let terraform = ($env.TERRAFORM_BIN? | default terraform)
    let bootstrap = $env.LAB_BOOTSTRAP
    let peer = $env.LAB_SERVER
    let worker = $env.LAB_WORKER
    let path = ($scratch | path join bin)
    mkdir $path ($scratch | path join test)
    with-env {CGO_ENABLED: "0"} {
        ^go build -o ($path | path join terraform-provider-k3s) ./cmd/terraform-provider-k3s
    }
    let rc = ($scratch | path join terraform.rc)
    $"provider_installation {
  dev_overrides {
    \"vbargl/k3s\" = ($path | to json --raw)
  }
  direct {}
}
" | save --raw $rc
    $env.TF_CLI_CONFIG_FILE = $rc
    cd ($scratch | path join test)
    cp ($env.FILE_PWD | path join fixtures main.tf) .
    for name in [bootstrap_address server_address worker_address bootstrap_pin server_pin worker_pin ssh_user] {
        let value = ($env | get $"LAB_($name | str uppercase)")
        require ($value | is-not-empty) $"Missing lab setting ($name)"
        load-env { $"TF_VAR_($name)": $value }
    }
    $env.TF_VAR_ssh_port = ($env.LAB_SSH_PORT? | default "22")
    let bad = (with-env {TF_VAR_bootstrap_pin: "SHA256:wrong"} {
        logged $terraform bad-key.log -- apply -auto-approve -no-color
    })
    require ($bad != 0 and (open --raw bad-key.log | str contains 'host operation "apply" failed')) "Incorrect SSH host key was not rejected as expected"

    $env.TF_LOG = "TRACE"
    $env.TF_LOG_PATH = ($scratch | path join provider.log)
    require ((logged $terraform plan.log -- plan -out=create.tfplan -no-color) == 0) "Terraform create plan failed"
    require ((logged $terraform apply.log -- apply -no-color create.tfplan) == 0) "Terraform apply failed"
    ^$terraform show -json create.tfplan | save --raw plan.json
    ^$terraform show -json | save --raw state.json
    let initial = (open state.json | get values.root_module.resources)
    require (($initial | get values.ready) == [true true true]) "Expected three ready nodes"
    require ((logged $terraform steady.log -- plan -detailed-exitcode -out=steady.tfplan -no-color) == 0) "Expected an unchanged Terraform plan"
    ^$terraform show -json steady.tfplan | save --raw steady.json

    # Revoke the worker's bootstrap token and restart it with existing node credentials.
    let worker_token = (^incus exec $worker -- cat /var/lib/k3s-coordinate/join-token | str trim)
    let token_id = ($worker_token | parse -r '::(?P<id>[a-z0-9]{6})\.[a-z0-9]+$' | get 0.id)
    ^incus exec $bootstrap -- k3s token delete $token_id | ignore
    ^incus exec $worker -- systemctl stop k3s-agent
    $env.TF_VAR_worker_revision = "1"
    require ((logged $terraform restart.log -- apply -auto-approve -no-color) == 0) "Worker restart apply failed"
    ^incus exec $worker -- systemctl is-active --quiet k3s-agent
    ^$terraform show -json | save --raw restarted.json
    let restarted = (open restarted.json | get values.root_module.resources | where index? == worker | first | get values)
    let original = ($initial | where index? == worker | first | get values)
    require ($restarted.ready and $restarted.id == $original.id) "Worker readiness or UID changed after restart"

    ^incus exec $bootstrap -- journalctl -u k3s --no-pager | save --raw bootstrap-journal.log
    ^incus exec $peer -- journalctl -u k3s --no-pager | save --raw server-journal.log
    ^incus exec $worker -- journalctl -u k3s-agent --no-pager | save --raw worker-journal.log
    let tokens = [
        (^incus exec $bootstrap -- cat /var/lib/rancher/k3s/server/token | str trim)
        (^incus exec $peer -- cat /var/lib/k3s-coordinate/join-token | str trim)
        $worker_token
    ]
    let secrets = ($tokens | each {|token|
        require ($token =~ '^K10[a-f0-9]{64}::.+$') "Missing test join token"
        [$token ($token | split row "::" | last)]
    } | flatten | uniq)
    let reports = [create.tfplan steady.tfplan terraform.tfstate plan.json state.json steady.json restarted.json $env.TF_LOG_PATH]
    scan ($reports | append [bad-key.log apply.log plan.log steady.log restart.log bootstrap-journal.log server-journal.log worker-journal.log]) $secrets
    let keys = (^incus exec $bootstrap -- cat /etc/rancher/k3s/k3s.yaml | from yaml | get users.user.client-key-data)
    require (($keys | is-not-empty) and ($keys | all {|key| $key != "" })) "Missing test kubeconfig private key"
    scan $reports $keys

    require ((logged $terraform remove.log -- destroy '-target=k3s_node.member["worker"]' -auto-approve -no-color) == 0) "Worker removal failed"
    let nodes = (^incus exec $bootstrap -- k3s kubectl get nodes -o json | from json | get items.metadata.name | sort)
    require ($nodes == [bootstrap peer]) "Expected only the two server nodes"
    print "Disposable VM Terraform test passed"
}

def main [] {
    let scratch = (^mktemp -d | str trim)
    try {
        cd ($env.FILE_PWD | path dirname)
        exercise $scratch
    } catch {|err|
        rm -rf $scratch
        error make {msg: $err.msg}
    }
    rm -rf $scratch
}
