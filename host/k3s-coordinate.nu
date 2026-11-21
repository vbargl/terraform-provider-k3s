#!/usr/bin/env -S nu --no-config-file --stdin

def fail-operation [] { error make {msg: "K3s host operation failed"} }

def run-command [argv: list<string>, input: string] {
    let result = ($input | ^timeout --kill-after=2 12 ...$argv | complete)
    if $result.exit_code != 0 { fail-operation }
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
        fail-operation
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
    if ($req.cluster_uid != "" and $cluster != $req.cluster_uid) or ($req.operation != "observe" and $req.cluster_uid == "") { fail-operation }
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
        if $target == "" or ($node != null and $actual != $target) { fail-operation }
        kubectl $runner [get "--raw=/readyz"] | ignore
        let token = match $req.role {
            "agent" => { do $runner [k3s token create --ttl 30m] "" }
            "server" => { open --raw ([$data server token] | path join) }
            _ => { fail-operation }
        }
        return {token: ($token | str trim)}
    }
    if $req.role != "agent" or $req.uid == "" { fail-operation }
    if $node != null {
        let options = {apiVersion: v1, kind: DeleteOptions, preconditions: {uid: $req.uid}}
        kubectl $runner [delete --raw $"/api/v1/nodes/($req.name)" -f "-"] ($options | to json --raw) | ignore
    }
    {}
}

def stop-service [runner: closure, unit: string] {
    do $runner [systemctl --no-block stop $unit] "" | ignore
    for attempt in 1..40 {
        let state = (do $runner [systemctl show --property=ActiveState --value $unit] "" | str trim)
        if $state in [inactive failed] { return }
        sleep 1sec
    }
    error make {msg: "K3s service stop still pending"}
}

export def handle [req: record, root: string, data: string, machine_id: string, runner: closure] {
    if $req.name !~ '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$' or $req.role not-in [bootstrap server agent] { fail-operation }
    if $req.role != "bootstrap" {
        let url = ($req.endpoint | url parse)
        if $req.endpoint !~ '^https://[^/@?#\s]+$' or ($url.host | is-empty) or $req.cluster_uid == "" { fail-operation }
    }
    if $req.operation in [observe token remove] { return (cluster-operation $req $data $runner) }

    let marker = ([$root intent.json] | path join)
    let config = ([$root k3s.yaml] | path join)
    let token_file = ([$root join-token] | path join)
    let intent = {name: $req.name, role: $req.role, endpoint: $req.endpoint}
    let token = ($req.token? | default "")
    if $token != "" and ($req.role == "bootstrap" or $token !~ '^K10[a-f0-9]{64}::[!-~]+$') { fail-operation }
    let unit = if $req.role == "agent" { "k3s-agent.service" } else { "k3s.service" }
    let current = if ($marker | path exists) {
        open $marker
    } else {
        if $req.operation not-in [apply probe] or $req.uid != "" or (unmanaged-data $data) { fail-operation }
        if $req.operation == "probe" { return {machine_id: $machine_id, needs_token: true} }
        let initial = {...$intent, cluster_uid: $req.cluster_uid, uid: ""}
        save-private $root $marker ($initial | to json --raw)
        $initial
    }
    if ($current | select name role endpoint) != $intent { fail-operation }
    if $req.cluster_uid != "" and $current.cluster_uid not-in ["" $req.cluster_uid] { fail-operation }
    if $req.uid != "" and $current.uid not-in ["" $req.uid] { fail-operation }

    let stopped = ($current.stopped? | default false)
    match $req.operation {
        "probe" => {
            if $stopped { fail-operation }
            let missing = (($token_file | path type) != "file")
            let expired = if not $missing and $req.role == "agent" and $current.uid == "" {
                ((date now) - (ls -l $token_file | first | get modified)) >= 30min
            } else { false }
            let active = if $expired {
                try {
                    do $runner [systemctl is-active --quiet $unit] "" | ignore
                    true
                } catch { false }
            } else { false }
            return {machine_id: $machine_id, needs_token: ($current.uid == "" and ($missing or ($expired and not $active)))}
        }
        "local" => {
            if $stopped or not ($config | path exists) { fail-operation }
            do $runner [systemctl is-active --quiet $unit] "" | ignore
            return {uid: $current.uid, machine_id: $machine_id}
        }
        "seal" => {
            if $stopped or $req.cluster_uid == "" or $req.uid == "" { fail-operation }
            save-private $root $marker ($current | merge {cluster_uid: $req.cluster_uid, uid: $req.uid} | to json --raw)
            return {}
        }
        "stop" => {
            if $req.role != "agent" or $req.uid == "" or $current.uid != $req.uid or $current.cluster_uid != $req.cluster_uid { fail-operation }
            save-private $root $marker ($current | upsert stopped true | to json --raw)
            ^rm -f -- $config
            stop-service $runner $unit
            ^rm -f -- $token_file
            return {}
        }
        "apply" => {}
        _ => { fail-operation }
    }
    if $stopped or ($current.uid != "" and ($data | path type) != "dir") { fail-operation }
    let started = ($current.started? | default false)
    let bootstrap = ($req.role == "bootstrap")
    if ($req.role != "agent") and ($started or $current.uid != "") and ([$data server db etcd] | path join | path type) != "dir" { fail-operation }
    let action = if not $bootstrap and $token != "" and ($token_file | path exists) and ((open --raw $token_file | str trim) != $token) { "restart" } else { "start" }
    let options = if $bootstrap {
        {node-name: $req.name, cluster-init: true}
    } else {
        if $token != "" {
            save-private $root $token_file $"($token)(char nl)"
        }
        if ($token_file | path type) != "file" { fail-operation }
        {node-name: $req.name, server: $req.endpoint, token-file: $token_file}
    }
    save-private $root $config ($options | to json --raw)
    if $bootstrap and not $started {
        save-private $root $marker ($current | upsert started true | to json --raw)
    }
    do $runner [systemctl --no-block $action $unit] "" | ignore
    {machine_id: $machine_id}
}

export def check-service-start [root: string, data: string] {
    let current = (open ([$root intent.json] | path join))
    if ($current.stopped? | default false) or ([$root k3s.yaml] | path join | path type) != "file" { fail-operation }
    if $current.uid != "" and ($data | path type) != "dir" { fail-operation }
    if $current.uid != "" and $current.role != "agent" and ([$data server db etcd] | path join | path type) != "dir" { fail-operation }
}

def main [--check-service-start, --locked] {
    $env.PATH = ["/run/current-system/sw/bin" "/usr/local/bin" "/usr/bin" "/bin"]
    let payload = ($in | ^head -c 8193 | into binary)
    let root = "/var/lib/k3s-coordinate"
    let data = "/var/lib/rancher/k3s"
    try {
        if ($payload | bytes length) > 8192 { fail-operation }
        if $check_service_start {
            check-service-start $root $data
        } else if $locked {
            let machine = (open --raw /etc/machine-id | str trim)
            handle ($payload | decode utf-8 | from json) $root $data $machine {|args, input| run-command $args $input } | to json --raw
        } else {
            ^install -d -m 0700 -- $root
            let lock = ([$root lock] | path join)
            let script = $env.CURRENT_FILE
            let request = ($payload | decode utf-8 | from json)
            let child = [env -i "PATH=/run/current-system/sw/bin:/usr/local/bin:/usr/bin:/bin" $nu.current-exe --no-config-file --stdin $script --locked]
            let result = if $request.operation in [observe token remove] {
                $payload | ^timeout --kill-after=5 50 ...$child | complete
            } else {
                $payload | ^timeout --kill-after=5 55 flock -w 5 -E 75 -x -- $lock ...$child | complete
            }
            if $result.exit_code in [75 124 137] {
                print --stderr "K3s host operation timed out or lock is busy"
                exit 75
            }
            if $result.exit_code != 0 { fail-operation }
            print --no-newline $result.stdout
        }
    } catch {|err|
        if $err.msg == "K3s service stop still pending" {
            print --stderr "K3s service stop still pending"
            exit 75
        }
        print --stderr "K3s host operation failed"
        exit 1
    }
}
