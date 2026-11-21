#!/usr/bin/env -S nu --no-config-file --stdin

def reject [] { error make {msg: "K3s host operation failed"} }

def run-command [argv: list<string>, input: string] {
    let result = ($input | ^timeout 25 ...$argv | complete)
    if $result.exit_code != 0 { reject }
    $result.stdout
}

def kubectl [runner: closure, args: list<string>, input: string = ""] {
    do $runner ([k3s kubectl "--request-timeout=10s"] | append $args) $input
}

def save-private [root: string, target: string, content: string] {
    let temporary = (^mktemp $"--tmpdir=($root)" | str trim)
    try {
        $content | save --force --raw $temporary
        ^sync $temporary
        ^mv -T -- $temporary $target
        ^sync $root
    } catch {
        ^rm -f -- $temporary
        reject
    }
}

def unmanaged-data [data: string] {
    if not ($data | path exists) { return false }
    if ($data | path type) != "dir" or ([$data agent images] | path join | path type) != "dir" { return true }
    let unexpected = (^find $data -mindepth 1 '(' -type l -o '(' ! -path $"($data)/agent" ! -path $"($data)/agent/images" ! -path $"($data)/agent/images/*" ')' ')' -print -quit)
    ($unexpected | is-not-empty)
}

def cluster-operation [req: record, data: string, runner: closure] {
    let cluster = (kubectl $runner [get namespace kube-system -o json] | from json | get metadata.uid)
    if ($req.cluster_uid != "" and $cluster != $req.cluster_uid) or ($req.operation != "observe" and $req.cluster_uid == "") { reject }
    let raw = (kubectl $runner [get node $req.name "--ignore-not-found" -o json])
    let node = if ($raw | is-empty) { null } else { $raw | from json }
    let target = ($req.machine_id? | default "")
    let actual = ($node.status?.nodeInfo?.machineID? | default "")
    if $req.operation == "observe" {
        kubectl $runner [get "--raw=/readyz"] | ignore
        if $node == null { return {cluster_uid: $cluster} }
        let control = ("node-role.kubernetes.io/control-plane" in ($node.metadata?.labels? | default {} | columns))
        let ready = ($node.status?.conditions? | default [] | any {|condition| $condition.type == "Ready" and $condition.status == "True" })
        return {cluster_uid: $cluster, uid: $node.metadata.uid, ready: ($ready and $control == ($req.role != "agent") and $target != "" and $target == $actual)}
    }
    if $req.operation == "token" {
        if $target == "" or ($node != null and $actual != $target) { reject }
        kubectl $runner [get "--raw=/readyz"] | ignore
        let token = match $req.role {
            "agent" => { do $runner [k3s token create --ttl 30m] "" }
            "server" => { open --raw ([$data server token] | path join) }
            _ => { reject }
        }
        return {token: ($token | str trim)}
    }
    if $req.role != "agent" or $req.uid == "" { reject }
    if $node != null {
        let options = {apiVersion: v1, kind: DeleteOptions, preconditions: {uid: $req.uid}}
        kubectl $runner [delete --raw $"/api/v1/nodes/($req.name)" -f "-"] ($options | to json --raw) | ignore
    }
    {}
}

export def handle [req: record, root: string, data: string, machine_id: string, runner: closure] {
    if $req.name !~ '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$' or $req.role not-in [bootstrap server agent] { reject }
    if $req.role != "bootstrap" {
        let url = ($req.endpoint | url parse)
        if $req.endpoint !~ '^https://[^/@?#\s]+$' or ($url.host | is-empty) or $req.cluster_uid == "" { reject }
    }
    if $req.operation in [observe token remove] { return (cluster-operation $req $data $runner) }
    if $req.operation == "probe" { return {machine_id: $machine_id} }

    let marker = ([$root intent.json] | path join)
    let config = ([$root k3s.yaml] | path join)
    let token_file = ([$root join-token] | path join)
    let intent = {name: $req.name, role: $req.role, endpoint: $req.endpoint}
    let current = if ($marker | path exists) {
        open $marker
    } else {
        if $req.operation != "apply" or $req.uid != "" or (unmanaged-data $data) { reject }
        let initial = {...$intent, cluster_uid: $req.cluster_uid, uid: ""}
        save-private $root $marker ($initial | to json --raw)
        $initial
    }
    if ($current | select name role endpoint) != $intent { reject }
    if $req.cluster_uid != "" and $current.cluster_uid not-in ["" $req.cluster_uid] { reject }
    if $req.uid != "" and $current.uid not-in ["" $req.uid] { reject }

    let unit = if $req.role == "agent" { "k3s-agent.service" } else { "k3s.service" }
    let stopped = ($current.stopped? | default false)
    match $req.operation {
        "local" => {
            if $stopped or not ($config | path exists) { reject }
            do $runner [systemctl is-active --quiet $unit] "" | ignore
            return {uid: $current.uid, machine_id: $machine_id}
        }
        "seal" => {
            if $stopped or $req.cluster_uid == "" or $req.uid == "" { reject }
            save-private $root $marker ($current | merge {cluster_uid: $req.cluster_uid, uid: $req.uid} | to json --raw)
            return {}
        }
        "stop" => {
            if $req.role != "agent" or $req.uid == "" or $current.uid != $req.uid or $current.cluster_uid != $req.cluster_uid { reject }
            save-private $root $marker ($current | upsert stopped true | to json --raw)
            ^rm -f -- $config
            do $runner [systemctl stop $unit] "" | ignore
            ^rm -f -- $token_file
            return {}
        }
        "apply" => {}
        _ => { reject }
    }
    if $stopped or ($current.uid != "" and ($data | path type) != "dir") { reject }
    let started = ($current.started? | default false)
    let bootstrap = ($req.role == "bootstrap")
    if $bootstrap and $started and not ([$data server db] | path join | path exists) { reject }
    let token = ($req.token? | default "")
    let action = if not $bootstrap and $token != "" and ($token_file | path exists) and ((open --raw $token_file | str trim) != $token) { "restart" } else { "start" }
    let options = if $bootstrap {
        {node-name: $req.name, cluster-init: true}
    } else {
        if $token != "" {
            if $token !~ '^K10[a-f0-9]{64}::[!-~]+$' { reject }
            save-private $root $token_file $"($token)(char nl)"
        }
        if ($token_file | path type) != "file" { reject }
        {node-name: $req.name, server: $req.endpoint, token-file: $token_file}
    }
    save-private $root $config ($options | to json --raw)
    if $bootstrap and not $started {
        save-private $root $marker ($current | upsert started true | to json --raw)
    }
    do $runner [systemctl --no-block $action $unit] "" | ignore
    {machine_id: $machine_id}
}

def main [--check-service-start, --locked] {
    $env.PATH = ["/run/current-system/sw/bin" "/usr/local/bin" "/usr/bin" "/bin"]
    let payload = ($in | ^head -c 8193 | into binary)
    let root = "/var/lib/k3s-coordinate"
    let data = "/var/lib/rancher/k3s"
    try {
        if ($payload | bytes length) > 8192 { reject }
        if $check_service_start {
            let current = (open ([$root intent.json] | path join))
            if ($current.stopped? | default false) or ([$root k3s.yaml] | path join | path type) != "file" { reject }
            if $current.uid != "" and ($data | path type) != "dir" { reject }
            if $current.uid != "" and $current.role == "bootstrap" and ([$data server db etcd] | path join | path type) != "dir" { reject }
        } else if $locked {
            let machine = (open --raw /etc/machine-id | str trim)
            handle ($payload | decode utf-8 | from json) $root $data $machine {|args, input| run-command $args $input } | to json --raw
        } else {
            ^install -d -m 0700 -- $root
            let lock = ([$root lock] | path join)
            let script = $env.CURRENT_FILE
            let result = ($payload | ^flock -w 30 -x -- $lock env -i "PATH=/run/current-system/sw/bin:/usr/local/bin:/usr/bin:/bin" $nu.current-exe --no-config-file --stdin $script --locked | complete)
            if $result.exit_code != 0 { reject }
            print --no-newline $result.stdout
        }
    } catch {
        print --stderr "K3s host operation failed"
        exit 1
    }
}
