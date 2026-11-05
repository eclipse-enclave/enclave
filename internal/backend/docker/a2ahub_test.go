// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

func TestHubAddressFor(t *testing.T) {
	used := map[netip.Addr]string{netip.MustParseAddr("172.18.0.30"): "other"}
	for _, tc := range []struct {
		subnet string
		used   map[netip.Addr]string
		want   string
		err    string
	}{
		{subnet: "172.18.0.0/28", want: "172.18.0.14"},
		{subnet: "172.18.0.16/28", used: used, want: "172.18.0.29"},
		{subnet: "10.89.4.0/24", want: "10.89.4.254"},
		{subnet: "172.20.0.0/16", want: "172.20.255.254"},
		{subnet: "172.18.0.4/30", err: "too small"},
		{subnet: "", err: "no IPv4 subnet"},
		{subnet: "fd00::/64", err: "no IPv4 subnet"},
	} {
		got, err := hubAddressFor(tc.subnet, tc.used)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("hubAddressFor(%q) error = %v, want %q", tc.subnet, err, tc.err)
			}
			continue
		}
		if err != nil || got.String() != tc.want {
			t.Fatalf("hubAddressFor(%q) = %v, %v; want %s", tc.subnet, got, err, tc.want)
		}
	}
	full := map[netip.Addr]string{}
	for _, last := range []byte{2, 3, 4, 5, 6} {
		full[netip.AddrFrom4([4]byte{10, 0, 0, last})] = "x"
	}
	if _, err := hubAddressFor("10.0.0.0/29", full); err == nil {
		t.Fatal("hubAddressFor on a full subnet succeeded")
	}
}

// hubRunArgv renders the hub run through the shared wrapper against a stub CLI.
func hubRunArgv(t *testing.T, spec backend.HubSpec, selinux bool) string {
	t.Helper()
	logPath := stubCLI(t, "docker", "")
	config, hostConfig, err := hubRunConfig(spec, backend.HubAttachment{Network: "net-orch", IPv4: "172.18.0.14"}, selinux)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dockercmd.RunDetached(context.Background(), config, hostConfig, HubContainerName(spec.Group)); err != nil {
		t.Fatal(err)
	}
	calls := stubCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("hub run calls = %v", calls)
	}
	return calls[0]
}

func TestHubRunArgsHardening(t *testing.T) {
	dir := t.TempDir()
	spec := backend.HubSpec{
		Group: "review", GroupGeneration: "3f9c0a1b2c3d4e5f", Image: "enclave-a2a-hub:latest",
		SnapshotDir: filepath.Join(dir, "snapshots"), StatusDir: filepath.Join(dir, "status"), AuditDir: filepath.Join(dir, "audit"),
		Env:    []backend.EnvVar{{Name: a2a.EnvHubLogMode, Value: "full"}, {Name: a2a.EnvHubProxyUID, Value: "0"}},
		Labels: map[string]string{peergroup.LabelHub: "false", "extra": "1"},
	}
	joined := hubRunArgv(t, spec, false)
	for _, want := range []string{
		"run --detach --name enclave-a2a-hub-review ",
		"--network net-orch --ip 172.18.0.14 --read-only ",
		"--security-opt no-new-privileges --cap-add NET_ADMIN --cap-add NET_RAW --cap-add SETUID --cap-add SETGID --cap-add KILL --cap-add DAC_READ_SEARCH --cap-drop ALL",
		"--sysctl net.ipv4.ip_forward=0 --tmpfs /run --tmpfs /tmp",
		"--mount type=bind,source=" + spec.SnapshotDir + ",target=" + peergroup.HubSnapshotMountPath + ",readonly",
		"--mount type=bind,source=" + spec.StatusDir + ",target=" + peergroup.HubStatusMountPath + " ",
		"--label enclave.a2a.group=review --label enclave.a2a.group_generation=3f9c0a1b2c3d4e5f --label enclave.a2a.hub=true",
		"--env ENCLAVE_A2A_GROUP_GENERATION=3f9c0a1b2c3d4e5f",
		"--env ENCLAVE_A2A_HUB_PORT=41820 --env ENCLAVE_A2A_PROXY_UID=65532",
		"--env ENCLAVE_A2A_LOG=full ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hub args missing %q:\n%s", want, joined)
		}
	}
	if !strings.HasSuffix(joined, " enclave-a2a-hub:latest") {
		t.Fatalf("hub args do not end with the image:\n%s", joined)
	}
	for _, unwanted := range []string{"--userns", "--restart", "ENCLAVE_A2A_PROXY_UID=0", "--privileged"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("hub args contain %q:\n%s", unwanted, joined)
		}
	}
	if selinuxArgs := hubRunArgv(t, spec, true); !strings.Contains(selinuxArgs, "--volume "+spec.SnapshotDir+":"+peergroup.HubSnapshotMountPath+":ro,z") {
		t.Fatalf("SELinux hub args = %v", selinuxArgs)
	}
	if _, _, err := hubRunConfig(spec, backend.HubAttachment{Network: "net-orch", IPv4: "172.18.0.14/28"}, false); err == nil {
		t.Fatal("hub args accepted a CIDR as the static address")
	}
}

func remapFiles(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if data, ok := files[path]; ok {
			return []byte(data), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestSecretsOwnerPlanFor(t *testing.T) {
	rootless := dockercmd.SystemInfo{SecurityOptions: []string{"name=seccomp", "name=rootless"}}
	// Fixtures from a daemon with "userns-remap": "default".
	remapped := dockercmd.SystemInfo{DockerRootDir: "/var/lib/docker/231072.231072", SecurityOptions: []string{"name=apparmor", "name=seccomp,profile=builtin", "name=userns"}}
	remappedCustomRoot := dockercmd.SystemInfo{DockerRootDir: "/srv/docker", SecurityOptions: []string{"name=userns"}}
	subids := remapFiles(map[string]string{
		"/etc/subuid": "tobias:100000:65536\ndockremap:165536:65536\n",
		"/etc/subgid": "tobias:100000:65536\ndockremap:200000:65536\n",
	})
	shortRange := remapFiles(map[string]string{"/etc/subuid": "dockremap:165536:1000\n", "/etc/subgid": "dockremap:165536:1000\n"})
	noFiles := remapFiles(nil)
	for _, tc := range []struct {
		name     string
		info     dockercmd.SystemInfo
		podman   bool
		euid     int
		readFile func(string) ([]byte, error)
		want     secretsOwnerPlan
		err      bool
	}{
		{name: "rootless podman", info: rootless, podman: true, euid: 1000, want: secretsOwnerPlan{mode: secretsOwnerUnshare, uid: 65532, gid: 65532}},
		{name: "rootful docker as user", euid: 1000, want: secretsOwnerPlan{mode: secretsOwnerHelper, uid: 65532, gid: 65532}},
		{name: "rootless docker", info: rootless, euid: 1000, want: secretsOwnerPlan{mode: secretsOwnerHelper, uid: 65532, gid: 65532}},
		{name: "rootful as root", euid: 0, want: secretsOwnerPlan{mode: secretsOwnerLocal, uid: 65532, gid: 65532}},
		{name: "userns-remap from the root dir", info: remapped, euid: 1000, readFile: noFiles, want: secretsOwnerPlan{mode: secretsOwnerHelper, uid: 231072 + 65532, gid: 231072 + 65532, userns: "host"}},
		{name: "userns-remap as root", info: remapped, euid: 0, readFile: noFiles, want: secretsOwnerPlan{mode: secretsOwnerLocal, uid: 231072 + 65532, gid: 231072 + 65532}},
		{name: "userns-remap from subordinate ids", info: remappedCustomRoot, euid: 1000, readFile: subids, want: secretsOwnerPlan{mode: secretsOwnerHelper, uid: 165536 + 65532, gid: 200000 + 65532, userns: "host"}},
		{name: "userns-remap range too short", info: remappedCustomRoot, euid: 1000, readFile: shortRange, err: true},
		{name: "userns-remap without a range", info: remappedCustomRoot, euid: 1000, readFile: noFiles, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := secretsOwnerPlanFor(tc.info, tc.podman, tc.euid, tc.readFile)
			if (err != nil) != tc.err || (!tc.err && got != tc.want) {
				t.Fatalf("secretsOwnerPlanFor() = %+v, %v; want %+v (err %v)", got, err, tc.want, tc.err)
			}
		})
	}
}

func writeStagedSecrets(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".staging-3")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, peergroup.SecretsFile)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSecretsHelperRunsChownWithOnlyCapChown(t *testing.T) {
	logPath := stubCLI(t, "docker", "")
	path := writeStagedSecrets(t)
	if err := secretsOwnerFor(secretsOwnerPlan{mode: secretsOwnerHelper, uid: 65532, gid: 65532}, "enclave-a2a-hub:latest")(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("secrets mode = %v, %v", info.Mode(), err)
	}
	calls := stubCalls(t, logPath)
	want := "run --user 0:0 --entrypoint chown --rm --network none --read-only --log-driver none --mount type=bind,source=" + filepath.Dir(path) + ",target=/staging --security-opt no-new-privileges --cap-add CHOWN --cap-drop ALL enclave-a2a-hub:latest 65532:65532 /staging/secrets.json"
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("helper calls = %v\nwant %s", calls, want)
	}
}

func TestSecretsHelperUnderUsernsRemapRunsInHostNamespace(t *testing.T) {
	logPath := stubCLI(t, "docker", "")
	path := writeStagedSecrets(t)
	plan := secretsOwnerPlan{mode: secretsOwnerHelper, uid: 231072 + 65532, gid: 231072 + 65532, userns: "host"}
	if err := secretsOwnerFor(plan, "enclave-a2a-hub:latest")(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	calls := stubCalls(t, logPath)
	if len(calls) != 1 || !strings.Contains(calls[0], " --userns host ") || !strings.HasSuffix(calls[0], " 296604:296604 /staging/secrets.json") {
		t.Fatalf("helper calls = %v", calls)
	}
}

func TestSecretsUnshareChownsThroughPodman(t *testing.T) {
	logPath := stubCLI(t, "podman", "")
	path := filepath.Join(t.TempDir(), peergroup.SecretsFile)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secretsOwnerFor(secretsOwnerPlan{mode: secretsOwnerUnshare, uid: 65532, gid: 65532}, "")(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	calls := stubCalls(t, logPath)
	if len(calls) != 1 || calls[0] != "unshare chown 65532:65532 "+path {
		t.Fatalf("unshare calls = %v", calls)
	}
}

func TestSignalWorkerSendsRequestOnStdin(t *testing.T) {
	logPath := stubCLI(t, "docker", `cat > "$STUB_CALL_LOG.stdin"
printf '{"op":"status","ok":true,"quarantined":false}'
`)
	req := a2a.CtlRequest{Op: a2a.CtlStatus, Principal: a2a.Principal{Group: "review", Member: "orch"}}
	res, err := (&Backend{}).SignalWorker(context.Background(), backend.SessionRef{Name: "worker"}, req)
	if err != nil || !res.Response.OK || res.Response.Quarantined == nil || *res.Response.Quarantined {
		t.Fatalf("SignalWorker() = %+v, %v", res, err)
	}
	calls := stubCalls(t, logPath)
	if len(calls) != 1 || calls[0] != "exec --interactive worker enclave-a2a-agent ctl" {
		t.Fatalf("calls = %v", calls)
	}
	stdin, err := os.ReadFile(logPath + ".stdin")
	if err != nil || !strings.Contains(string(stdin), `"op":"status"`) || !strings.Contains(string(stdin), `"member":"orch"`) {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
}

func TestSignalWorkerReportsRefusal(t *testing.T) {
	stubCLI(t, "docker", `cat >/dev/null
printf '{"op":"depart","ok":false,"error":"unknown principal"}'
exit 1
`)
	_, err := (&Backend{}).SignalWorker(context.Background(), backend.SessionRef{Name: "worker"}, a2a.CtlRequest{Op: a2a.CtlDepart})
	if err == nil || !strings.Contains(err.Error(), "unknown principal") {
		t.Fatalf("SignalWorker() error = %v", err)
	}
}

func TestPeerGroupIsolationThreshold(t *testing.T) {
	usePodmanCLI(t)
	previous := networkDriverOptions
	t.Cleanup(func() { networkDriverOptions = previous })
	for value, ok := range map[string]bool{"strict": true, "true": true, "": false, "false": false} {
		networkDriverOptions = func(context.Context, string) (map[string]string, error) {
			return map[string]string{podmanIsolateOption: value}, nil
		}
		if err := checkPeerGroupNetworkIsolation(context.Background(), "net"); (err == nil) != ok {
			t.Fatalf("isolate=%q: error = %v, want ok %v", value, err, ok)
		}
	}
}

func TestRemoveOwnedSessionNetworkDisconnectsHubFirst(t *testing.T) {
	restoreNetworkGlobals(t)
	info := dockercmd.NetworkInspectResponse{
		ID: "net-id", Name: sessionNetworkName("session"),
		Labels:     sessionNetworkLabels(backend.SessionMeta{Name: "session", ProjectHash: "project"}),
		Containers: map[string]dockercmd.NetworkEndpoint{"hub": {Name: "enclave-a2a-hub-review"}},
	}
	var order []string
	networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) { return info, nil }
	sessionRuntimeExists = func(context.Context, string) (bool, error) { return false, nil }
	previousHubs, previousDisconnect := labeledHubEndpoints, hubNetworkDisconnect
	t.Cleanup(func() { labeledHubEndpoints, hubNetworkDisconnect = previousHubs, previousDisconnect })
	labeledHubEndpoints = func(_ context.Context, names []string) map[string]bool {
		hubs := map[string]bool{}
		for _, name := range names {
			if strings.HasPrefix(name, peergroup.HubContainerPrefix) {
				hubs[name] = true
			}
		}
		return hubs
	}
	hubNetworkDisconnect = func(_ context.Context, network, container string, force bool) error {
		if force {
			t.Fatal("teardown forced the hub disconnect")
		}
		order = append(order, "disconnect "+container)
		info.Containers = nil
		return nil
	}
	networkRemove = func(_ context.Context, ref string) error {
		order = append(order, "rm "+ref)
		return nil
	}
	if err := removeOwnedSessionNetwork(context.Background(), sessionNetworkRef{Name: info.Name, ID: "net-id", Container: "session", ProjectHash: "project"}, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "disconnect enclave-a2a-hub-review,rm net-id" {
		t.Fatalf("teardown order = %v", order)
	}
}

func TestDockerConfigCarriesPeerGroupLabelsAndEnv(t *testing.T) {
	req := backend.Request{
		Session: backend.SessionMeta{Tool: "claude", Name: "enclave-claude-ab12cd34ef56-w1"},
		Env:     []backend.EnvVar{{Name: "A", Value: "1"}},
		PeerGroup: &backend.PeerGroupSession{
			Env:    []backend.EnvVar{{Name: a2a.EnvRole, Value: "worker"}},
			Labels: map[string]string{peergroup.LabelGroup: "review"},
		},
	}
	spec := New(Options{}).dockerConfig(req)
	if spec.config.Labels[peergroup.LabelGroup] != "review" {
		t.Fatalf("labels = %v", spec.config.Labels)
	}
	// Peer group env, tokens included, goes through the env file only.
	if strings.Join(spec.config.Env, ",") != "A=1" {
		t.Fatalf("env = %v", spec.config.Env)
	}
}

func TestSecretEnvFileKeepsTokensOffTheArgv(t *testing.T) {
	logPath := stubCLI(t, "docker", `for arg; do
  if [ "$prev" = "--env-file" ]; then stat -c %a "$arg" > "$STUB_CALL_LOG.mode"; cat "$arg" > "$STUB_CALL_LOG.env"; fi
  prev=$arg
done
printf '%064d\n' 0
`)
	envFile, err := writeSecretEnvFile([]backend.EnvVar{
		{Name: a2a.EnvRole, Value: "worker"},
		{Name: a2a.EnvInboundToken, Value: "in-secret"},
		{Name: a2a.EnvOutboundToken, Value: "out-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := &dockercmd.ContainerConfig{Image: "img", Env: []string{"A=1"}, EnvFiles: []string{envFile.path}}
	if _, err := dockercmd.RunDetachedInteractive(context.Background(), config, &dockercmd.HostConfig{}, "w1"); err != nil {
		t.Fatal(err)
	}
	envFile.remove()
	calls := stubCalls(t, logPath)
	if len(calls) != 1 || strings.Contains(calls[0], "secret") || !strings.Contains(calls[0], "--env-file "+envFile.path) {
		t.Fatalf("run calls = %v", calls)
	}
	env, _ := os.ReadFile(logPath + ".env")
	mode, _ := os.ReadFile(logPath + ".mode")
	if string(env) != "ENCLAVE_A2A_ROLE=worker\nENCLAVE_A2A_INBOUND_TOKEN=in-secret\nENCLAVE_A2A_OUTBOUND_TOKEN=out-secret\n" || strings.TrimSpace(string(mode)) != "600" {
		t.Fatalf("env file = %q mode %q", env, mode)
	}
	if _, err := os.Stat(filepath.Dir(envFile.path)); !os.IsNotExist(err) {
		t.Fatalf("env file dir still exists: %v", err)
	}
	if _, err := writeSecretEnvFile([]backend.EnvVar{{Name: "X", Value: "a\nb"}}); err == nil {
		t.Fatal("env file accepted a value with a newline")
	}
}

func TestPrepareRunRejectsUnpreparedPeerGroupSession(t *testing.T) {
	_, err := New(Options{}).prepareRun(context.Background(), backend.Request{PeerGroup: &backend.PeerGroupSession{Group: "review", Member: "w1"}})
	if err == nil || !strings.Contains(err.Error(), "not prepared") {
		t.Fatalf("prepareRun() error = %v", err)
	}
}

func TestRemoveOwnedSessionNetworkKeepsForeignEndpoint(t *testing.T) {
	restoreNetworkGlobals(t)
	info := dockercmd.NetworkInspectResponse{
		ID: "net-id", Name: sessionNetworkName("session"),
		Labels:     map[string]string{model.NetworkLabelManaged: "true", model.NetworkLabelContainer: "session", model.NetworkLabelProjectHash: "project"},
		Containers: map[string]dockercmd.NetworkEndpoint{"x": {Name: "enclave-a2a-hub-fake"}},
	}
	networkInspect = func(context.Context, string) (dockercmd.NetworkInspectResponse, error) { return info, nil }
	previousHubs := labeledHubEndpoints
	t.Cleanup(func() { labeledHubEndpoints = previousHubs })
	labeledHubEndpoints = func(context.Context, []string) map[string]bool { return map[string]bool{} }
	networkRemove = func(context.Context, string) error {
		t.Fatal("removed a network with an unlabeled hub-named endpoint")
		return nil
	}
	if err := removeOwnedSessionNetwork(context.Background(), sessionNetworkRef{Name: info.Name, ID: "net-id", Container: "session", ProjectHash: "project"}, 0); err != nil {
		t.Fatal(err)
	}
}
