#!/usr/bin/env -S nu --no-config-file

use std/assert
use k3s-coordinate.nu handle

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
        let bootstrap = ($req | merge {role: bootstrap, endpoint: "", cluster_uid: ""})
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
    print "Six host-helper tests passed"
}
