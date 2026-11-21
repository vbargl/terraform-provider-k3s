#!/usr/bin/env -S nu --no-config-file

use std/assert
use k3s-coordinate.nu [handle check-service-start]

def fixture [test: closure] {
    let root = (^mktemp -d | str trim)
    let data = ($root | path join data)
    let req = {
        operation: apply, name: worker, role: agent, endpoint: "https://cluster.example",
        cluster_uid: test-cluster, uid: "", token: $"K10('a' | fill -c a -w 64)::abcdef.abcdefghijklmnop"
    }
    try {
        do $test $req $root $data
    } catch {|err|
        rm -rf $root
        error make {msg: $err.msg}
    }
    rm -rf $root
}

def main [] {
    fixture {|req, root, data|
        assert error { handle $req $root $data machine {|args, input| error make {msg: "simulated service failure"} } }
        assert ($root | path join intent.json | path exists)
        assert equal (^stat -c %a ($root | path join join-token) | str trim) "600"
        handle ($req | upsert token "") $root $data machine {|args, input|
            assert equal $args [systemctl --no-block start k3s-agent.service]
        } | ignore
        for artifact in [k3s.yaml intent.json] {
            assert (not (open --raw ($root | path join $artifact) | str contains $req.token))
        }
    }
    fixture {|req, root, data|
        let bootstrap = ($req | merge {role: bootstrap, endpoint: "", cluster_uid: "", token: ""})
        handle $bootstrap $root $data machine {|args, input| } | ignore
        let retry = (try { handle $bootstrap $root $data machine {|args, input| "unexpected" } } catch { null })
        assert equal $retry null
    }
    fixture {|req, root, data|
        mkdir $data
        assert error { handle $req $root $data machine {|args, input| } }
        assert (not ($root | path join intent.json | path exists))
        mkdir ($data | path join agent images)
        handle $req $root $data machine {|args, input| } | ignore
    }
    fixture {|req, root, data|
        handle ($req | merge {operation: remove, uid: original}) $root $data machine {|args, input|
            match ($args | get 3) {
                "get" => {
                    if ($args | get 4) == "namespace" { '{"metadata":{"uid":"test-cluster"}}' } else { '{"metadata":{"uid":"other"}}' }
                }
                "delete" => {
                    assert equal ($input | from json | get preconditions.uid) "original"
                    assert equal ($args | get 5) "/api/v1/nodes/worker"
                }
                _ => { error make {msg: "unexpected kubectl invocation"} }
            }
        } | ignore
    }
    fixture {|req, root, data|
        let invocation = ($root | path join unexpected)
        assert error {
            handle ($req | merge {operation: token, machine_id: local}) $root $data machine {|args, input|
                match ($args | get 4) {
                    "namespace" => { '{"metadata":{"uid":"test-cluster"}}' }
                    "node" => { '{"status":{"nodeInfo":{"machineID":"other"}}}' }
                    _ => { "unexpected" | save $invocation }
                }
            }
        }
        assert (not ($invocation | path exists))
    }
    fixture {|req, root, data|
        let invocation = ($root | path join unexpected)
        assert error {
            handle ($req | upsert operation token) $root $data machine {|args, input|
                if ($args | get 4) == "namespace" { '{"metadata":{"uid":"wrong"}}' } else { "unexpected" | save $invocation }
            }
        }
        assert (not ($invocation | path exists))
    }
    fixture {|req, root, data|
        let no_command = {|args, input| error make {msg: "unexpected command"} }
        assert error { handle ($req | upsert token malformed) $root $data machine $no_command }
        assert (not ($root | path join intent.json | path exists))
        let probe = (handle ($req | merge {operation: probe, token: ""}) $root $data machine $no_command)
        assert $probe.needs_token
        handle $req $root $data machine {|args, input| } | ignore
        let probe = (handle ($req | upsert operation probe) $root $data machine $no_command)
        assert (not $probe.needs_token)
        assert error { handle ($req | upsert endpoint "https://other.example") $root $data machine $no_command }
        assert error { handle ($req | upsert endpoint "https://cluster.example/path") $root $data machine $no_command }
        let newer = ($req | update token {|row| $"($row.token)x" })
        handle $newer $root $data machine {|args, input|
            assert equal $args [systemctl --no-block restart k3s-agent.service]
        } | ignore
        let sealed = ($req | merge {uid: node-uid, token: ""})
        handle ($sealed | upsert operation seal) $root $data machine $no_command | ignore
        let local = (handle ($sealed | upsert operation local) $root $data machine {|args, input|
            assert equal $args [systemctl is-active --quiet k3s-agent.service]
        })
        assert equal $local.uid "node-uid"
        handle ($sealed | upsert operation stop) $root $data machine {|args, input|
            if ($args | get 1) == "--no-block" {
                assert equal $args [systemctl --no-block stop k3s-agent.service]
            } else {
                assert equal $args [systemctl show --property=ActiveState --value k3s-agent.service]
                "inactive"
            }
        } | ignore
        assert (not ($root | path join join-token | path exists))
        assert error { handle $req $root $data machine $no_command }
    }
    fixture {|req, root, data|
        handle $req $root $data machine {|args, input| } | ignore
        ^touch -d "31 minutes ago" ($root | path join join-token)
        let probe = ($req | merge {operation: probe, token: ""})
        let inactive = (handle $probe $root $data machine {|args, input|
            assert equal $args [systemctl is-active --quiet k3s-agent.service]
            error make {msg: "inactive"}
        })
        assert $inactive.needs_token
        let active = (handle $probe $root $data machine {|args, input| })
        assert (not $active.needs_token)
    }
    fixture {|req, root, data|
        let server = ($req | upsert role server)
        handle $server $root $data machine {|args, input| } | ignore
        mkdir $data
        handle ($server | merge {operation: seal, uid: node-uid, token: ""}) $root $data machine {|args, input| } | ignore
        assert error { check-service-start $root $data }
        mkdir ($data | path join server db etcd)
        check-service-start $root $data
    }
    let helper = ($env.FILE_PWD | path join k3s-coordinate.nu)
    let oversized = ("x" | fill -c x -w 8193 | ^$nu.current-exe --no-config-file --stdin $helper --locked | complete)
    assert equal $oversized.exit_code 1
    assert equal ($oversized.stderr | str trim) "K3s host operation failed"
    print "Host-helper tests passed"
}
