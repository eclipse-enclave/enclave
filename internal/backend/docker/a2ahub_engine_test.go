// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build docker

package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

// Run with: go test -tags docker ./internal/backend/docker -run Engine
// ENCLAVE_TEST_ENGINE=podman selects podman; ENCLAVE_TEST_IMAGE overrides the
// alpine base of the stand-ins; ENCLAVE_TEST_HUB_IMAGE runs a real hub image
// (built from Dockerfile.a2ahub) instead of the stand-in hub.

// standinHubScript acks every revision the way both hub readers do: one
// epoch per start, both status files rewritten on start and on SIGHUP.
const standinHubScript = `epoch=$(head -c8 /dev/urandom | od -An -tx1 | tr -d ' \n')
write() {
  applied=0
  manifest="$ENCLAVE_A2A_HUB_SNAPSHOT_DIR/current/manifest.json"
  if [ -f "$manifest" ]; then applied=$(sed -n 's/.*"revision": *\([0-9]*\).*/\1/p' "$manifest" | head -n1); fi
  for r in supervisor proxy; do
    tmp="$ENCLAVE_A2A_HUB_STATUS_DIR/.$r.tmp"
    printf '{"schemaVersion":"1","reader":"%s","groupGeneration":"%s","epoch":"%s","appliedRevision":%s,"rejectedRevision":0,"error":"","updatedAt":"2026-11-05T00:00:00Z"}' "$r" "$ENCLAVE_A2A_GROUP_GENERATION" "$epoch" "${applied:-0}" > "$tmp"
    mv "$tmp" "$ENCLAVE_A2A_HUB_STATUS_DIR/$r.json"
  done
}
trap write HUP
trap 'exit 0' TERM
write
while :; do sleep 1 & wait $!; done
`

// standinHubCallScript stands in for `enclave-a2a-hub call`: it echoes what it
// received, then holds the stream open before the last event so a test can
// tell streaming from buffering. Path /fail exits 1 with a reason on stderr.
const standinHubCallScript = `#!/bin/sh
[ "$1" = call ] || exit 2
if [ "$2" = /fail ]; then echo "call: proxy refused" >&2; exit 1; fi
IFS= read -r cred
body=$(cat)
printf 'uid=%s\npath=%s\ncred=%s\nbody=%s\n\n' "$(id -u)" "$2" "$cred" "$body"
sleep 3
printf 'done\n'
`

// standinGatewayScript logs the overlay marker the real gateway logs on reload.
const standinGatewayScript = `trap 'f=/etc/enclave-a2a-overlay/overlay.json; g=$(sed -n "s/.*\"generation\": *\([0-9]*\).*/\1/p" $f); if grep -q "\"active\": *true" $f; then echo "A2A overlay applied (generation=$g)"; else echo "A2A overlay cleared (generation=$g)"; fi' HUP
trap 'exit 0' TERM
while :; do sleep 1 & wait $!; done
`

// engineCLI runs the engine CLI for test setup and probes.
func engineCLI(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, dockercmd.Binary(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", dockercmd.Binary(), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

type engineA2A struct {
	t      *testing.T
	ctx    context.Context
	prefix string
	home   string
	image  string
	b      *Backend
	p      *PeerGroups
}

func engineBaseImage() string {
	if image := os.Getenv("ENCLAVE_TEST_IMAGE"); image != "" {
		return image
	}
	return "docker.io/library/alpine:3.20"
}

func newEngineA2A(t *testing.T) *engineA2A {
	t.Helper()
	return newEngineA2APrefixed(t, "a2a-a7")
}

// newEngineA2APrefixed names every engine resource <base>-<random>.
func newEngineA2APrefixed(t *testing.T, base string) *engineA2A {
	t.Helper()
	if engine := os.Getenv("ENCLAVE_TEST_ENGINE"); engine != "" {
		previous := dockercmd.Binary()
		dockercmd.SetBinary(engine)
		t.Cleanup(func() { dockercmd.SetBinary(previous) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	if _, err := engineCLI(ctx, "version"); err != nil {
		t.Fatalf("%s is not usable: %v", dockercmd.Binary(), err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	e := &engineA2A{t: t, ctx: ctx, prefix: base + "-" + hex.EncodeToString(suffix), home: t.TempDir()}
	if image := os.Getenv("ENCLAVE_TEST_HUB_IMAGE"); image != "" {
		e.image = image
	} else {
		e.image = e.prefix + "-hub-standin"
		e.commitImage(e.image, standinHubScript)
	}
	e.b = New(Options{Host: model.Host{Home: e.home}})
	p, err := e.b.PeerGroups(ctx, PeerGroupConfig{HubImage: e.image, CommitTimeout: 10 * time.Second, DepartureAckTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e.p = p
	t.Cleanup(func() { e.cleanupEngine() })
	return e
}

func (e *engineA2A) run(args ...string) string {
	e.t.Helper()
	out, err := engineCLI(e.ctx, args...)
	if err != nil {
		e.t.Fatalf("%v", err)
	}
	return out
}

// commitImage builds a stand-in image without a registry: write the script
// into a container of the base image and commit it with an entrypoint.
func (e *engineA2A) commitImage(image, script string) {
	e.t.Helper()
	builder := e.prefix + "-builder"
	e.run("run", "--name", builder, "--entrypoint", "/bin/sh", engineBaseImage(), "-c", "cat > /standin.sh <<'EOF'\n"+script+"EOF\n"+
		"mkdir -p /usr/local/bin && cat > "+a2aHubBinary+" <<'EOF'\n"+standinHubCallScript+"EOF\nchmod 0755 "+a2aHubBinary+"\n")
	defer func() { _, _ = engineCLI(context.Background(), "rm", "-f", builder) }()
	e.run("commit", "--change", `ENTRYPOINT ["/bin/sh", "/standin.sh"]`, builder, image)
}

func (e *engineA2A) cleanupEngine() {
	ctx := context.Background()
	// Podman refuses to remove a gateway while a session shares its namespace.
	for pass := 0; pass < 3; pass++ {
		if out, err := engineCLI(ctx, "ps", "-a", "--format", "{{.Names}}"); err == nil {
			for _, name := range strings.Fields(out) {
				if strings.Contains(name, e.prefix) {
					_, _ = engineCLI(ctx, "rm", "-f", name)
				}
			}
		}
	}
	if out, err := engineCLI(ctx, "network", "ls", "--format", "{{.Name}}"); err == nil {
		for _, name := range strings.Fields(out) {
			if strings.HasPrefix(name, e.prefix) {
				_, _ = engineCLI(ctx, "network", "rm", name)
			}
		}
	}
	if os.Getenv("ENCLAVE_TEST_HUB_IMAGE") == "" {
		_, _ = engineCLI(ctx, "rmi", "-f", e.image)
	}
}

type engineMember struct {
	name    string
	network string
	gateway string
	join    *Join
}

func (e *engineA2A) group() string { return e.prefix }

func (e *engineA2A) spec(member string, role a2a.Role) JoinSpec {
	session := e.prefix + "-" + member
	return JoinSpec{
		Group: e.group(), Role: role, Member: member, SessionContainer: session,
		Tool: "claude", ProjectHash: "ab12cd34ef56", ConfigStoreKey: "default",
		ConfigStoreDir:     filepath.Join(e.home, "store", member),
		ContainerConfigDir: "/home/agent/.claude",
		ProjectPath:        filepath.Join(e.home, "src", member),
		MountMode:          peergroup.MountReadWrite,
	}
}

// joinMember mirrors prepareRun with stand-ins for the gateway and tool.
func (e *engineA2A) joinMember(member string, role a2a.Role) (*engineMember, error) {
	e.t.Helper()
	spec := e.spec(member, role)
	j, err := e.p.Join(e.ctx, spec)
	if err != nil {
		return nil, err
	}
	info, err := dockerInfo(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	ref, err := e.b.ensureSessionNetwork(e.ctx, backend.SessionMeta{Name: spec.SessionContainer, ProjectHash: spec.ProjectHash}, info)
	if err != nil {
		_ = j.Abort(e.ctx)
		e.t.Fatalf("session network: %v", err)
	}
	m := &engineMember{name: member, network: ref.Name, gateway: spec.SessionContainer + "-gateway", join: j}
	if err := j.OnSessionNetwork(e.ctx, ref.Name); err != nil {
		_ = j.Fail(e.ctx)
		return m, err
	}
	e.run("run", "--detach", "--name", m.gateway, "--network", ref.Name,
		"--volume", j.Session().OverlayDir+":"+peergroup.GatewayOverlayDir+":ro",
		"--entrypoint", "/bin/sh", engineBaseImage(), "-c", standinGatewayScript)
	if err := j.OnGateway(e.ctx, m.gateway, ref.Name); err != nil {
		_ = j.Fail(e.ctx)
		return m, err
	}
	args := []string{"run", "--detach", "--name", spec.SessionContainer, "--network", "container:" + m.gateway}
	for key, value := range j.Session().Labels {
		args = append(args, "--label", key+"="+value)
	}
	envFile, err := writeSecretEnvFile(j.Session().Env)
	if err != nil {
		e.t.Fatal(err)
	}
	e.run(append(args, "--env-file", envFile.path, "--stop-signal", "KILL", engineBaseImage(), "sleep", "600")...)
	envFile.remove()
	if token := e.run("exec", spec.SessionContainer, "printenv", a2a.EnvInboundToken); token == "" {
		e.t.Fatalf("session %s has no inbound token from the env file", spec.SessionContainer)
	}
	j.Started()
	return m, nil
}

func (e *engineA2A) hubHostConfig() map[string]any {
	e.t.Helper()
	var hostConfig map[string]any
	if err := json.Unmarshal([]byte(e.run("container", "inspect", "--format", "{{json .HostConfig}}", HubContainerName(e.group()))), &hostConfig); err != nil {
		e.t.Fatal(err)
	}
	return hostConfig
}

func lastUsable(t *testing.T, subnet string) string {
	t.Helper()
	addr, err := hubAddressFor(subnet, nil)
	if err != nil {
		t.Fatal(err)
	}
	return addr.String()
}

func TestEngineA2AJoinLeaveTeardown(t *testing.T) {
	e := newEngineA2A(t)
	orch, err := e.joinMember("orch", a2a.RoleOrchestrator)
	if err != nil {
		t.Fatalf("join orch: %v", err)
	}
	worker, err := e.joinMember("w1", a2a.RoleWorker)
	if err != nil {
		t.Fatalf("join w1: %v", err)
	}

	rec, err := e.p.store.Load(e.group())
	if err != nil || rec.Committed != 2 || len(rec.Members) != 2 {
		t.Fatalf("record after two joins = %+v, %v", rec, err)
	}
	wantEngine := backend.NameDocker
	if dockercmd.IsPodman() {
		wantEngine = backend.NamePodman
	}
	if rec.Engine == nil || rec.Engine.Name != wantEngine || rec.Engine.Root == "" {
		t.Fatalf("record engine = %+v, want %s with its storage root", rec.Engine, wantEngine)
	}
	hubName := HubContainerName(e.group())
	endpoints, err := hubEndpoints(e.ctx, hubName)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rec.Members {
		endpoint, ok := findHubEndpoint(endpoints, m.Network)
		if !ok || endpoint.IPAddress != m.HubAddress || m.HubAddress != lastUsable(t, m.Subnet) {
			t.Fatalf("hub endpoint on %s = %+v (record %s, subnet %s)", m.Network, endpoint, m.HubAddress, m.Subnet)
		}
	}

	hostConfig := e.hubHostConfig()
	if hostConfig["ReadonlyRootfs"] != true {
		t.Fatalf("hub rootfs is writable: %v", hostConfig["ReadonlyRootfs"])
	}
	// Podman reports CapAdd relative to its defaults, so check the kernel's view.
	if status := e.run("exec", hubName, "grep", "-E", "^(CapBnd|NoNewPrivs)", "/proc/1/status"); !strings.Contains(status, "00000000000030e4") || !strings.Contains(status, "NoNewPrivs:\t1") {
		t.Fatalf("hub capabilities = %q, want bounding set 0x30e4 and no-new-privileges", status)
	}
	if forward := e.run("exec", hubName, "cat", "/proc/sys/net/ipv4/ip_forward"); forward != "0" {
		t.Fatalf("hub ip_forward = %q", forward)
	}
	if _, err := engineCLI(e.ctx, "exec", hubName, "touch", "/rootfs-probe"); err == nil {
		t.Fatal("hub rootfs accepted a write")
	}
	if uidMap := e.run("exec", hubName, "cat", "/proc/self/uid_map"); dockercmd.IsPodman() && strings.Contains(uidMap, "1000") && strings.HasPrefix(strings.TrimSpace(uidMap), "0 1 1000") {
		t.Fatalf("hub runs under keep-id: %q", uidMap)
	}

	stateRoot := config.HostStateRootDir(e.home)
	secrets := filepath.Join(peergroup.SnapshotDir(stateRoot, e.group(), rec.Committed), peergroup.SecretsFile)
	info, err := os.Stat(secrets)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0o400 || stat == nil || int(stat.Uid) == os.Getuid() {
		t.Fatalf("secrets.json mode %v owner %v: still the host user's", info.Mode(), stat)
	}
	t.Logf("secrets.json owner on the host: uid %d", stat.Uid)

	hubs, err := e.b.ListHubs(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hub := range hubs {
		if hub.Ref.Group == e.group() {
			found = hub.Running && len(hub.Attachments) == 2 && hub.GroupGeneration == string(rec.GroupGeneration)
		}
	}
	if !found {
		t.Fatalf("ListHubs() = %+v", hubs)
	}
	if hubs := labeledHubEndpoints(e.ctx, []string{hubName, orch.gateway}); !hubs[hubName] || hubs[orch.gateway] {
		t.Fatalf("labeledHubEndpoints() = %v", hubs)
	}

	// A failed connect --ip (address held by another endpoint) rolls the join back.
	failing := e.spec("w2", a2a.RoleWorker)
	j, err := e.p.Join(e.ctx, failing)
	if err != nil {
		t.Fatal(err)
	}
	sysInfo, _ := dockerInfo(e.ctx)
	ref, err := e.b.ensureSessionNetwork(e.ctx, backend.SessionMeta{Name: failing.SessionContainer, ProjectHash: failing.ProjectHash}, sysInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.OnSessionNetwork(e.ctx, ref.Name); err != nil {
		t.Fatal(err)
	}
	hubAddr := j.member().HubAddress
	e.run("run", "--detach", "--name", e.prefix+"-blocker", "--network", ref.Name, "--ip", hubAddr, "--stop-signal", "KILL", engineBaseImage(), "sleep", "600")
	gw := failing.SessionContainer + "-gateway"
	e.run("run", "--detach", "--name", gw, "--network", ref.Name, "--volume", j.Session().OverlayDir+":"+peergroup.GatewayOverlayDir+":ro", "--entrypoint", "/bin/sh", engineBaseImage(), "-c", standinGatewayScript)
	previousUsed := usedNetworkAddresses
	usedNetworkAddresses = func(context.Context, string, string) (map[netip.Addr]string, error) {
		return map[netip.Addr]string{}, nil
	}
	err = j.OnGateway(e.ctx, gw, ref.Name)
	usedNetworkAddresses = previousUsed
	if err == nil {
		t.Fatal("connect to an address in use succeeded")
	}
	t.Logf("failed connect: %v (stale endpoint: %v)", err, errors.Is(err, dockercmd.ErrStaleEndpoint))
	if err := j.Fail(e.ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	rec, err = e.p.store.Load(e.group())
	if err != nil || len(rec.Members) != 2 || rec.Committed != 2 || rec.Pending != nil {
		t.Fatalf("record after failed join = %+v, %v", rec, err)
	}
	if attached, err := hubAttached(e.ctx, hubName, ref.Name); err != nil || attached {
		t.Fatalf("hub attached to the failed member's bridge after rollback: %v, %v", attached, err)
	}
	if _, err := os.Stat(j.Session().OverlayDir); !os.IsNotExist(err) {
		t.Fatalf("overlay of the failed join survives: %v", err)
	}

	// A stopped hub whose stale endpoint config would block the restart
	// starts again through restartHub, and keeps its static addresses.
	stop := 2 * time.Second
	if err := dockercmd.ContainerStop(e.ctx, hubName, &stop); err != nil {
		t.Fatal(err)
	}
	state, err := e.b.inspectHub(e.ctx, e.group())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.b.restartHub(e.ctx, e.p.hubSpec(rec), state); err != nil {
		t.Fatalf("restart hub: %v", err)
	}
	if endpoint, ok := findHubEndpoint(mustEndpoints(t, e.ctx, hubName), orch.network); !ok || endpoint.IPAddress != rec.Members[0].HubAddress {
		t.Fatalf("hub address after restart = %+v", endpoint)
	}

	// Worker leave: removal commits, hub leaves the bridge, overlay is cleared.
	res, err := e.p.Leave(e.ctx, LeaveSpec{Group: e.group(), Member: worker.name})
	if err != nil || res.Degraded || !res.Left {
		t.Fatalf("Leave(w1) = %+v, %v", res, err)
	}
	if attached, _ := hubAttached(e.ctx, hubName, worker.network); attached {
		t.Fatal("hub still attached to the departed worker's bridge")
	}
	if logs := e.run("logs", worker.gateway); !strings.Contains(logs, "A2A overlay cleared (generation=2)") {
		t.Fatalf("gateway did not clear the overlay: %q", logs)
	}

	// Teardown of the orchestrator's session: the hub endpoint is owned and
	// disconnected first; network rm without -f leaves the hub in place.
	for _, name := range []string{orch.join.spec.SessionContainer, orch.gateway} {
		e.run("rm", "-f", name)
	}
	if err := removeOwnedSessionNetwork(e.ctx, mustNetworkRef(t, e.ctx, orch.network, orch.join.spec), 20); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if exists, _ := e.b.networkExists(e.ctx, orch.network); exists {
		t.Fatal("orchestrator bridge survives teardown")
	}
	if state, err := e.b.inspectHub(e.ctx, e.group()); err != nil || !state.Exists {
		t.Fatalf("teardown removed the hub: %+v, %v", state, err)
	}

	// GC: the worker's session is gone too, so the hub ends up empty and is removed.
	for _, name := range []string{worker.join.spec.SessionContainer, worker.gateway} {
		e.run("rm", "-f", name)
	}
	gcPeerGroupHubs(e.ctx)
	if state, err := e.b.inspectHub(e.ctx, e.group()); err != nil || state.Exists {
		t.Fatalf("GC kept the empty hub: %+v, %v", state, err)
	}

	// Reconcile settles the record: the orchestrator's session is gone.
	rres, err := e.p.Reconcile(e.ctx, e.group())
	if err != nil {
		t.Fatalf("Reconcile() = %+v, %v", rres, err)
	}
	if _, err := e.p.store.Load(e.group()); !errors.Is(err, peergroup.ErrGroupNotFound) {
		t.Fatalf("group record after reconcile: %v (%+v)", err, rres)
	}
}

func mustEndpoints(t *testing.T, ctx context.Context, container string) map[string]dockercmd.ContainerNetworkEndpoint {
	t.Helper()
	endpoints, err := hubEndpoints(ctx, container)
	if err != nil {
		t.Fatal(err)
	}
	return endpoints
}

func mustNetworkRef(t *testing.T, ctx context.Context, network string, spec JoinSpec) sessionNetworkRef {
	t.Helper()
	info, err := networkInspect(ctx, network)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := newSessionNetworkRef(info.Name, info.ID, spec.SessionContainer, spec.ProjectHash)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestEngineA2AUsernsRemapFileAccess needs a Docker daemon with userns-remap;
// run the whole Engine suite there with ENCLAVE_TEST_USERNS_REMAP=1.
func TestEngineA2AUsernsRemapFileAccess(t *testing.T) {
	if os.Getenv("ENCLAVE_TEST_USERNS_REMAP") != "1" {
		t.Skip("set ENCLAVE_TEST_USERNS_REMAP=1 against a Docker daemon with userns-remap")
	}
	e := newEngineA2A(t)
	info, err := dockerInfo(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(info.SecurityOptions, " "), "name=userns") {
		t.Fatalf("daemon does not report userns-remap: %v", info.SecurityOptions)
	}
	uidBase, _, err := dockerRemapBase(info, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}

	staging := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(staging, peergroup.SecretsFile)
	if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := secretsOwnerFromInfo(info, e.image)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner(e.ctx, secrets); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(secrets)
	if err != nil {
		t.Fatal(err)
	}
	if sys, _ := stat.Sys().(*syscall.Stat_t); sys == nil || int(sys.Uid) != uidBase+peergroup.HubProxyUID {
		t.Fatalf("secrets.json owner = %v, want uid %d", stat.Sys(), uidBase+peergroup.HubProxyUID)
	}
	e.run("run", "--rm", "--user", "65532", "--volume", staging+":/s:ro", engineBaseImage(), "cat", "/s/"+peergroup.SecretsFile)

	previous := syscall.Umask(0o077)
	overlayDir := filepath.Join(e.home, "overlay", "session")
	err = ensureOverlayDir(overlayDir)
	syscall.Umask(previous)
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(overlayDir, peergroup.OverlayFile)
	if err := peergroup.WriteOverlay(overlay, peergroup.Overlay{SchemaVersion: peergroup.SchemaVersion, Generation: 1, Group: e.group(), Member: "w1"}); err != nil {
		t.Fatal(err)
	}
	e.run("run", "--rm", "--volume", overlayDir+":/o:ro", engineBaseImage(), "cat", "/o/"+peergroup.OverlayFile)

	shared := filepath.Join(t.TempDir(), "status")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"0", "65532"} {
		e.run("run", "--rm", "--user", user, "--volume", shared+":/st", engineBaseImage(), "sh", "-c", "echo {} > /st/probe-"+user+" && chmod 0644 /st/probe-"+user)
		if _, err := os.ReadFile(filepath.Join(shared, "probe-"+user)); err != nil {
			t.Fatalf("host cannot read a status file written by container uid %s: %v", user, err)
		}
	}
}
