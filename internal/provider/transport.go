package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
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
}

type responseBuffer struct{ buffer bytes.Buffer }

func (b *responseBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 8192 {
		return 0, errors.New("host response exceeded protocol limit")
	}
	return b.buffer.Write(p)
}

func call(ctx context.Context, host, user, pin string, req request) (response, error) {
	var result response
	failure := fmt.Errorf("host operation %q failed for node %q", req.Operation, req.Name)
	if !strings.HasPrefix(pin, "SHA256:") {
		return result, failure
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	dialer := net.Dialer{}
	socket, err := dialer.DialContext(ctx, "unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return result, errors.New("an accessible SSH agent is required")
	}
	defer func() { _ = socket.Close() }()
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return result, failure
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		_ = socket.Close()
	})
	defer stop()
	config := &ssh.ClientConfig{
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(socket).Signers)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != pin {
				return errors.New("host key mismatch")
			}
			return nil
		},
	}
	c, channels, requests, err := ssh.NewClientConn(conn, host, config)
	if err != nil {
		return result, failure
	}
	client := ssh.NewClient(c, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return result, failure
	}
	defer func() { _ = session.Close() }()
	payload, err := json.Marshal(req)
	if err != nil {
		return result, failure
	}
	session.Stdin = bytes.NewReader(payload)
	var output responseBuffer
	session.Stdout = &output
	if err := session.Run("k3s-coordinate"); err != nil || json.Unmarshal(output.buffer.Bytes(), &result) != nil {
		return response{}, failure
	}
	return result, nil
}

func (n node) request(op string) request {
	return request{Operation: op, Name: n.Name.ValueString(), Role: n.Role.ValueString(),
		Endpoint: n.Endpoint.ValueString(), ClusterUID: n.ClusterUID.ValueString(), UID: n.ID.ValueString()}
}
func (n node) target(ctx context.Context, req request) (response, error) {
	return call(ctx, n.Host.ValueString(), n.User.ValueString(), n.Pin.ValueString(), req)
}
func (n node) anchor(ctx context.Context, req request) (response, error) {
	if n.Role.ValueString() == "bootstrap" {
		return n.target(ctx, req)
	}
	return call(ctx, n.Anchor.ValueString(), n.User.ValueString(), n.AnchorPin.ValueString(), req)
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
	req := n.request("apply")
	if !dnsLabel.MatchString(req.Name) {
		return response{}, errors.New("name must be a DNS label")
	}
	switch req.Role {
	case "bootstrap":
		if req.Endpoint != "" || n.Anchor.ValueString() != "" || n.AnchorPin.ValueString() != "" {
			return response{}, errors.New("bootstrap must omit registration and anchor fields")
		}
	case "server", "agent":
		endpoint, err := url.Parse(req.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" ||
			endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
			n.Anchor.ValueString() == "" || n.AnchorPin.ValueString() == "" || req.ClusterUID == "" {
			return response{}, errors.New("joins require an HTTPS origin, pinned anchor and cluster UID")
		}
		local, err := n.target(ctx, n.request("local"))
		if (err != nil || local.UID == "") && req.UID == "" {
			target, err := n.target(ctx, n.request("probe"))
			if err != nil {
				return response{}, err
			}
			tokenRequest := n.request("token")
			tokenRequest.MachineID = target.MachineID
			token, err := n.anchor(ctx, tokenRequest)
			if err != nil {
				return response{}, err
			}
			req.Token = token.Token
		}
	default:
		return response{}, errors.New("role must be bootstrap, server or agent")
	}
	local, err := n.target(ctx, req)
	if err != nil {
		return response{}, err
	}
	observation := n.request("observe")
	observation.MachineID = local.MachineID
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		result, err := n.anchor(wait, observation)
		if err == nil && result.Ready && result.UID != "" && result.ClusterUID != "" &&
			(req.ClusterUID == "" || result.ClusterUID == req.ClusterUID) &&
			(req.UID == "" || result.UID == req.UID) {
			localCheck := n.request("local")
			active, localErr := n.target(wait, localCheck)
			if localErr == nil && active.MachineID == observation.MachineID {
				seal := req
				seal.Operation, seal.ClusterUID, seal.UID, seal.Token = "seal", result.ClusterUID, result.UID, ""
				if _, err := n.target(wait, seal); err != nil {
					return response{}, err
				}
				return result, nil
			}
		}
		select {
		case <-wait.Done():
			return response{}, errors.New("node did not reach verified readiness")
		case <-ticker.C:
		}
	}
}

func remove(ctx context.Context, n node) error {
	if n.Role.ValueString() != "agent" {
		return errors.New("control-plane removal requires operator-led quorum and datastore maintenance")
	}
	if _, err := n.target(ctx, n.request("stop")); err != nil {
		return err
	}
	_, err := n.anchor(ctx, n.request("remove"))
	return err
}
