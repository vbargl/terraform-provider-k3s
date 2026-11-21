package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type request struct {
	Operation  string `json:"operation"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Endpoint   string `json:"endpoint"`
	ClusterUID string `json:"cluster_uid"`
	UID        string `json:"uid"`
	Token      string `json:"token,omitempty"`
	MachineID  string `json:"machine_id,omitempty"`
}
type response struct {
	ClusterUID string `json:"cluster_uid"`
	UID        string `json:"uid"`
	Ready      bool   `json:"ready"`
	Token      string `json:"token"`
	MachineID  string `json:"machine_id"`
	NeedsToken bool   `json:"needs_token"`
}

const protocolLimit = 8192

var errResponseLimit = errors.New("host response exceeded protocol limit")

type responseBuffer struct{ buffer bytes.Buffer }

func (b *responseBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > protocolLimit {
		return 0, errResponseLimit
	}
	return b.buffer.Write(p)
}

type failureKind uint8

const (
	permanentFailure failureKind = iota
	retryableFailure
	helperRejected
)

type transportError struct {
	message string
	kind    failureKind
}

func isFailure(err error, kind failureKind) bool {
	var failure *transportError
	return errors.As(err, &failure) && failure.kind == kind
}

func (e *transportError) Error() string { return e.message }

func call(ctx context.Context, host, user, pin, identity string, req request) (response, error) {
	failure := func(reason string, kind failureKind) (response, error) {
		return response{}, &transportError{fmt.Sprintf("host operation %q failed for node %q: %s", req.Operation, req.Name, reason), kind}
	}
	if !validPin(pin) {
		return failure("invalid SSH host fingerprint", permanentFailure)
	}
	const setupTimeout = 20 * time.Second
	const commandTimeout = 65 * time.Second
	ctx, cancel := context.WithTimeout(ctx, setupTimeout+commandTimeout)
	defer cancel()
	setup, cancelSetup := context.WithTimeout(ctx, setupTimeout)
	defer cancelSetup()
	setupDeadline, _ := setup.Deadline()
	dialer := net.Dialer{Timeout: 10 * time.Second}
	socket, err := dialer.DialContext(setup, "unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return failure("an accessible SSH agent is required", permanentFailure)
	}
	defer func() { _ = socket.Close() }()
	_ = socket.SetDeadline(setupDeadline)
	signers, err := agent.NewClient(socket).Signers()
	if err != nil {
		return failure("could not read SSH agent keys", permanentFailure)
	}
	if identity != "" {
		for _, signer := range signers {
			if ssh.FingerprintSHA256(signer.PublicKey()) == identity {
				signers = []ssh.Signer{signer}
				break
			}
		}
		if len(signers) != 1 || ssh.FingerprintSHA256(signers[0].PublicKey()) != identity {
			return failure("selected SSH identity is absent from the agent", permanentFailure)
		}
	}
	if len(signers) == 0 {
		return failure("the SSH agent has no keys", permanentFailure)
	}
	conn, err := dialer.DialContext(setup, "tcp", host)
	if err != nil {
		return failure("SSH connection unavailable", retryableFailure)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		_ = socket.Close()
	})
	defer stop()
	pinMismatch := false
	config := &ssh.ClientConfig{
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != pin {
				pinMismatch = true
				return errors.New("host key mismatch")
			}
			return nil
		},
	}
	_ = conn.SetDeadline(setupDeadline)
	c, channels, requests, err := ssh.NewClientConn(conn, host, config)
	if err != nil {
		var network net.Error
		switch {
		case pinMismatch:
			return failure("SSH host key mismatch", permanentFailure)
		case ctx.Err() != nil || errors.Is(err, io.EOF) || errors.As(err, &network):
			return failure("SSH handshake interrupted or timed out", retryableFailure)
		default:
			return failure("SSH authentication or security negotiation failed", permanentFailure)
		}
	}
	_ = conn.SetDeadline(time.Now().Add(commandTimeout))
	client := ssh.NewClient(c, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return failure("SSH session unavailable", retryableFailure)
	}
	defer func() { _ = session.Close() }()
	payload, err := json.Marshal(req)
	if err != nil || len(payload) > protocolLimit {
		return failure("invalid host request", permanentFailure)
	}
	session.Stdin = bytes.NewReader(payload)
	var output responseBuffer
	session.Stdout = &output
	if err := session.Run("k3s-coordinate"); err != nil {
		var remote *ssh.ExitError
		switch {
		case ctx.Err() != nil:
			return failure("SSH operation timed out or was canceled", retryableFailure)
		case errors.Is(err, errResponseLimit):
			return failure(errResponseLimit.Error(), permanentFailure)
		case errors.As(err, &remote) && remote.ExitStatus() == 75:
			return failure("host operation timed out or lock is busy", retryableFailure)
		case errors.As(err, &remote) && remote.ExitStatus() == 78:
			return failure("anchor cluster identity mismatch", permanentFailure)
		case errors.As(err, &remote):
			return failure("host helper rejected the operation", helperRejected)
		default:
			return failure("SSH session ended without a response", retryableFailure)
		}
	}
	var result response
	if !bytes.HasPrefix(bytes.TrimSpace(output.buffer.Bytes()), []byte("{")) || json.Unmarshal(output.buffer.Bytes(), &result) != nil {
		return failure("host helper returned an invalid response", permanentFailure)
	}
	return result, nil
}

type hostCall func(context.Context, request) (response, error)

func callWithRetry(ctx context.Context, operation hostCall, req request) (response, error) {
	for attempt := 0; ; attempt++ {
		result, err := operation(ctx, req)
		// Token creation is not idempotent after an ambiguous disconnection.
		if req.Operation == "token" || attempt == 2 || !isFailure(err, retryableFailure) {
			return result, err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return response{}, err
		case <-timer.C:
		}
	}
}

func (n node) request(op string) request {
	return request{Operation: op, Name: n.Name.ValueString(), Role: n.Role.ValueString(),
		Endpoint: n.Endpoint.ValueString(), ClusterUID: n.ClusterUID.ValueString(), UID: n.ID.ValueString()}
}
func (n node) target(ctx context.Context, req request) (response, error) {
	return call(ctx, n.Host.ValueString(), n.User.ValueString(), n.Pin.ValueString(), n.Identity.ValueString(), req)
}
func (n node) anchor(ctx context.Context, req request) (response, error) {
	if n.Role.ValueString() == "bootstrap" {
		return n.target(ctx, req)
	}
	return call(ctx, n.Anchor.ValueString(), n.User.ValueString(), n.AnchorPin.ValueString(), n.Identity.ValueString(), req)
}

func observe(ctx context.Context, n node) (response, error) {
	local, err := n.target(ctx, n.request("local"))
	if err != nil {
		return response{}, err
	}
	req := n.request("observe")
	req.MachineID = local.MachineID
	return n.anchor(ctx, req)
}

func reconcile(ctx context.Context, n node) (response, error) {
	if diagnostics := validateNode(n, false); diagnostics.HasError() {
		return response{}, errors.New(diagnostics.Errors()[0].Detail())
	}
	return reconcileNode(ctx, n, n.target, n.anchor)
}

func reconcileNode(ctx context.Context, n node, target, anchor hostCall) (response, error) {
	req := n.request("apply")
	if req.Role != "bootstrap" && req.UID == "" {
		probe, err := callWithRetry(ctx, target, n.request("probe"))
		if err != nil {
			return response{}, err
		}
		if probe.MachineID == "" {
			return response{}, errors.New("target did not report its machine identity")
		}
		if probe.NeedsToken {
			tokenRequest := n.request("token")
			tokenRequest.MachineID = probe.MachineID
			token, err := anchor(ctx, tokenRequest)
			if err != nil {
				return response{}, err
			}
			if token.Token == "" {
				return response{}, errors.New("anchor did not return a join token")
			}
			req.Token = token.Token
		}
	}
	local, err := callWithRetry(ctx, target, req)
	if err != nil {
		return response{}, err
	}
	if local.MachineID == "" {
		return response{}, errors.New("target did not report its machine identity")
	}
	observation := n.request("observe")
	observation.MachineID = local.MachineID
	wait, cancel := context.WithTimeout(ctx, n.readinessTimeout())
	defer cancel()
	for {
		result, err := anchor(wait, observation)
		if isFailure(err, permanentFailure) {
			return response{}, err
		}
		if req.ClusterUID != "" && result.ClusterUID != "" && result.ClusterUID != req.ClusterUID {
			return response{}, errors.New("anchor cluster identity mismatch")
		}
		if req.UID != "" && result.UID != "" && result.UID != req.UID {
			return response{}, errors.New("the cluster reports a different node UID")
		}
		if err == nil && result.Ready && result.UID != "" && result.ClusterUID != "" &&
			(req.ClusterUID == "" || result.ClusterUID == req.ClusterUID) &&
			(req.UID == "" || result.UID == req.UID) {
			active, localErr := target(wait, n.request("local"))
			if isFailure(localErr, permanentFailure) {
				return response{}, localErr
			}
			if localErr == nil && active.MachineID == observation.MachineID {
				seal := n.request("seal")
				seal.ClusterUID, seal.UID = result.ClusterUID, result.UID
				if _, err := callWithRetry(ctx, target, seal); err != nil {
					return response{}, err
				}
				return result, nil
			}
		}
		timer := time.NewTimer(5*time.Second + time.Duration(rand.IntN(2000))*time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			if err != nil {
				return response{}, fmt.Errorf("node did not reach verified readiness: %w", err)
			}
			return response{}, errors.New("node did not reach verified readiness")
		case <-timer.C:
		}
	}
}

func remove(ctx context.Context, n node) error {
	if n.Role.ValueString() != "agent" {
		return errors.New("control-plane removal requires operator-led quorum and datastore maintenance")
	}
	if _, err := callWithRetry(ctx, n.target, n.request("stop")); err != nil {
		return err
	}
	_, err := callWithRetry(ctx, n.anchor, n.request("remove"))
	return err
}
