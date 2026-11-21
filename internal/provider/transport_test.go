package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func testKey(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, signer
}

func startSSHAgent(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "agent")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	keyring := agent.NewKeyring()
	for _, key := range keys {
		if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", socket)
}

func startSSHHost(t *testing.T, host, client ssh.Signer, reply string, exitCode uint32) (string, <-chan request) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	received := make(chan request, 1)
	config := &ssh.ServerConfig{
		MaxAuthTries: 1,
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if ssh.FingerprintSHA256(key) != ssh.FingerprintSHA256(client.PublicKey()) {
				return nil, errors.New("test key rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(host)
	go func() {
		defer close(received)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer func() { _ = server.Close() }()
		go ssh.DiscardRequests(requests)
		channel := <-channels
		if channel == nil {
			return
		}
		stream, commands, err := channel.Accept()
		if err != nil {
			return
		}
		defer func() { _ = stream.Close() }()
		command := <-commands
		if command == nil {
			return
		}
		var exec struct{ Command string }
		if ssh.Unmarshal(command.Payload, &exec) != nil || exec.Command != "k3s-coordinate" {
			_ = command.Reply(false, nil)
			return
		}
		_ = command.Reply(true, nil)
		payload, err := io.ReadAll(stream)
		if err != nil {
			return
		}
		var req request
		if json.Unmarshal(payload, &req) != nil {
			return
		}
		received <- req
		_, _ = stream.Stderr().Write([]byte("untrusted test stderr"))
		_, _ = stream.Write([]byte(reply))
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, exitCode)
		_, _ = stream.SendRequest("exit-status", false, status)
	}()
	return listener.Addr().String(), received
}

func TestSSHTransport(t *testing.T) {
	_, host := testKey(t)
	wrong, _ := testKey(t)
	private, client := testKey(t)
	startSSHAgent(t, wrong, private)
	pin := ssh.FingerprintSHA256(host.PublicKey())
	identity := ssh.FingerprintSHA256(client.PublicKey())
	for _, test := range []struct {
		name, pin, identity, reply, expected string
		code                                 uint32
	}{
		{"selected key", pin, identity, `{"machine_id":"same-host"}`, "", 0},
		{"wrong pin", testPin, identity, "{}", "SSH host key mismatch", 0},
		{"missing client key", pin, testPin, "{}", "selected SSH identity is absent", 0},
		{"invalid JSON", pin, identity, "untrusted test payload", "invalid response", 0},
		{"oversize", pin, identity, strings.Repeat("x", protocolLimit+1), "exceeded protocol limit", 0},
		{"helper refusal", pin, identity, "{}", "host helper rejected", 1},
		{"lock contention", pin, identity, "{}", "lock is busy", 75},
	} {
		t.Run(test.name, func(t *testing.T) {
			address, received := startSSHHost(t, host, client, test.reply, test.code)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := call(ctx, address, "tester", test.pin, test.identity, request{Operation: "probe", Name: "worker", Token: "test-token"})
			if test.expected == "" {
				if err != nil || result.MachineID != "same-host" {
					t.Fatalf("unexpected response: %v, %v", result, err)
				}
				if req := <-received; req.Token != "test-token" {
					t.Fatal("request did not reach helper stdin")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.expected) || strings.Contains(err.Error(), "untrusted test") {
				t.Fatalf("expected sanitized error containing %q, got %v", test.expected, err)
			}
		})
	}
}

func TestReconcileJoinRetry(t *testing.T) {
	n := configuredNode("agent")
	for _, fresh := range []bool{true, false} {
		var targetCalls, anchorCalls []string
		sealAttempts := 0
		target := func(_ context.Context, req request) (response, error) {
			targetCalls = append(targetCalls, req.Operation)
			switch req.Operation {
			case "probe":
				return response{MachineID: "host", NeedsToken: fresh}, nil
			case "apply":
				if (req.Token == "test-token") != fresh {
					t.Fatal("unexpected token transfer")
				}
			case "seal":
				sealAttempts++
				if req.UID != "node" || req.ClusterUID != "cluster" || req.Token != "" {
					t.Fatal("seal must carry verified public identities only")
				}
				if !fresh && sealAttempts == 1 {
					return response{}, &transportError{"test disconnection", true}
				}
			}
			return response{MachineID: "host"}, nil
		}
		anchor := func(_ context.Context, req request) (response, error) {
			anchorCalls = append(anchorCalls, req.Operation)
			if req.MachineID != "host" {
				t.Fatal("anchor request missing target identity")
			}
			return response{Ready: true, UID: "node", ClusterUID: "cluster", Token: "test-token"}, nil
		}
		result, err := reconcileNode(context.Background(), n, target, anchor)
		if err != nil || result.UID != "node" {
			t.Fatalf("reconciliation failed: %v", err)
		}
		wantAnchor := []string{"observe"}
		if fresh {
			wantAnchor = []string{"token", "observe"}
		}
		wantTarget := []string{"probe", "apply", "local", "seal"}
		if !fresh {
			wantTarget = append(wantTarget, "seal")
		}
		if !reflect.DeepEqual(anchorCalls, wantAnchor) || !reflect.DeepEqual(targetCalls, wantTarget) {
			t.Fatalf("unexpected calls (fresh=%v): %v %v", fresh, anchorCalls, targetCalls)
		}
	}
}

func TestAmbiguousTokenRequestIsNotRetried(t *testing.T) {
	attempts := 0
	_, err := callWithRetry(context.Background(), func(context.Context, request) (response, error) {
		attempts++
		return response{}, &transportError{"test disconnection", true}
	}, request{Operation: "token"})
	if err == nil || attempts != 1 {
		t.Fatalf("expected one attempt and an error, got %d and %v", attempts, err)
	}
}
