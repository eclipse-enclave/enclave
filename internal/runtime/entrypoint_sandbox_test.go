// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxStubDir builds PATH stubs for the rootless sandbox launcher: id
// reports root, and unshare/chown record their invocations instead of needing
// real privileges.
func sandboxStubDir(t *testing.T, logDir string) string {
	t.Helper()
	dir := t.TempDir()
	stubs := map[string]string{
		"id": `#!/bin/sh
case "$1" in
  -u) echo 0 ;;
  -un) echo root ;;
  *) echo 0 ;;
esac
`,
		"unshare": `#!/bin/sh
printf '%s\n' "$*" >> ` + filepath.Join(logDir, "unshare.args") + `
readlink /proc/self/fd/0 >> ` + filepath.Join(logDir, "unshare.stdin") + `
exit 42
`,
		"chown": `#!/bin/sh
printf '%s\n' "$*" >> ` + filepath.Join(logDir, "chown.args") + `
exit 0
`,
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	return dir
}

func runSandboxEntrypoint(t *testing.T, env []string, stdin ...*os.File) (string, int) {
	t.Helper()
	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	cmd := exec.Command("bash", entrypointPath, "true")
	cmd.Env = env
	if len(stdin) == 1 {
		cmd.Stdin = stdin[0]
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("entrypoint: %v\noutput:\n%s", err, out)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

func TestEntrypointSandboxLauncherForksMappedNamespace(t *testing.T) {
	logDir := t.TempDir()
	etcDir := t.TempDir()
	runDir := filepath.Join(t.TempDir(), "enclave")
	writeFile(t, filepath.Join(etcDir, "passwd"), "root:x:0:0:root:/root:/bin/bash\nagent:x:0:0::/home/agent:/bin/bash\n")
	writeFile(t, filepath.Join(etcDir, "group"), "root:x:0:\nagent:x:0:\n")

	env := []string{
		"PATH=" + sandboxStubDir(t, logDir) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/root",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_UID=1234",
		"ENCLAVE_SANDBOX_GID=5678",
		"ENCLAVE_SANDBOX_ETC=" + etcDir,
		"ENCLAVE_SANDBOX_RUN_DIR=" + runDir,
	}
	out, code := runSandboxEntrypoint(t, env)
	// The launcher exits with the sandbox child's status; the unshare stub
	// exits 42.
	if code != 42 {
		t.Fatalf("expected launcher to propagate sandbox exit 42, got %d\noutput:\n%s", code, out)
	}

	unshareArgs, err := os.ReadFile(filepath.Join(logDir, "unshare.args"))
	if err != nil {
		t.Fatalf("unshare not invoked: %v\noutput:\n%s", err, out)
	}
	for _, want := range []string{"--user", "--mount", "--propagation private", "--map-user=1234", "--map-group=5678", "--keep-caps"} {
		if !strings.Contains(string(unshareArgs), want) {
			t.Fatalf("unshare args missing %q: %s", want, unshareArgs)
		}
	}

	assertFileContent(t, filepath.Join(etcDir, "passwd"), "root:x:0:0:root:/root:/bin/bash\nagent:x:1234:5678::/home/agent:/bin/bash\n")
	assertFileContent(t, filepath.Join(etcDir, "group"), "root:x:0:\nagent:x:5678:\n")

	pid, err := os.ReadFile(filepath.Join(runDir, "sandbox.pid"))
	if err != nil {
		t.Fatalf("sandbox pid file missing: %v", err)
	}
	if strings.TrimSpace(string(pid)) == "" {
		t.Fatalf("sandbox pid file empty")
	}
}

func TestEntrypointSandboxRejectsRuntimeUIDRemapCombination(t *testing.T) {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/root",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_UID=1000",
		"ENCLAVE_SANDBOX_GID=1000",
		"ENCLAVE_RUNTIME_UID=1000",
		"ENCLAVE_RUNTIME_GID=1000",
	}
	out, code := runSandboxEntrypoint(t, env)
	if code == 0 {
		t.Fatalf("expected failure for sandbox+remap combination\noutput:\n%s", out)
	}
}

func TestEntrypointSandboxRejectsZeroUID(t *testing.T) {
	logDir := t.TempDir()
	env := []string{
		"PATH=" + sandboxStubDir(t, logDir) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/root",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_UID=0",
		"ENCLAVE_SANDBOX_GID=0",
	}
	out, code := runSandboxEntrypoint(t, env)
	if code == 0 {
		t.Fatalf("expected failure for zero sandbox uid\noutput:\n%s", out)
	}
	if !strings.Contains(out, "nonzero numeric ENCLAVE_SANDBOX_UID") {
		t.Fatalf("unexpected error output:\n%s", out)
	}
}

// TestEntrypointSandboxPassesStdinToSandbox guards interactive sessions: the
// sandbox runs as a background job, and a non-interactive shell assigns
// /dev/null to an async command's stdin unless it is redirected explicitly, so
// without the redirect an interactive shell read EOF and exited immediately.
func TestEntrypointSandboxPassesStdinToSandbox(t *testing.T) {
	logDir := t.TempDir()
	etcDir := t.TempDir()
	writeFile(t, filepath.Join(etcDir, "passwd"), "agent:x:0:0::/home/agent:/bin/bash\n")
	writeFile(t, filepath.Join(etcDir, "group"), "agent:x:0:\n")

	// Stand in for the session TTY: any stdin that is not /dev/null proves the
	// descriptor was inherited rather than replaced.
	stdinPath := filepath.Join(t.TempDir(), "session-stdin")
	writeFile(t, stdinPath, "")
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin stand-in: %v", err)
	}
	defer stdin.Close()

	env := []string{
		"PATH=" + sandboxStubDir(t, logDir) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/root",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_UID=1000",
		"ENCLAVE_SANDBOX_GID=1000",
		"ENCLAVE_SANDBOX_ETC=" + etcDir,
		"ENCLAVE_SANDBOX_RUN_DIR=" + filepath.Join(t.TempDir(), "run"),
	}
	out, code := runSandboxEntrypoint(t, env, stdin)
	if code != 42 {
		t.Fatalf("expected launcher to fork the sandbox (stub exit 42), got %d\noutput:\n%s", code, out)
	}
	got, err := os.ReadFile(filepath.Join(logDir, "unshare.stdin"))
	if err != nil {
		t.Fatalf("sandbox stdin not recorded: %v\noutput:\n%s", err, out)
	}
	if strings.TrimSpace(string(got)) != stdinPath {
		t.Fatalf("sandbox stdin = %q, want the session stdin %q", strings.TrimSpace(string(got)), stdinPath)
	}
}

// TestEntrypointSandboxPointsResolverAtGatewayBeforeSealingEtc guards the DNS
// bypass this launcher originally shipped with: the sandbox remounts /etc
// read-only, so pointing resolv.conf at the gateway resolver has to happen in
// the outer phase. A silent failure there left the daemon's resolver in place
// and domain enforcement was skipped entirely.
func TestEntrypointSandboxPointsResolverAtGatewayBeforeSealingEtc(t *testing.T) {
	logDir := t.TempDir()
	etcDir := t.TempDir()
	resolvConf := filepath.Join(t.TempDir(), "resolv.conf")
	writeFile(t, filepath.Join(etcDir, "passwd"), "agent:x:0:0::/home/agent:/bin/bash\n")
	writeFile(t, filepath.Join(etcDir, "group"), "agent:x:0:\n")
	// Stand in for runtime-assets/net.sh, whose helper writes the resolver.
	netLib := filepath.Join(t.TempDir(), "net.sh")
	writeFile(t, netLib, "enclave_ensure_local_resolver() { printf 'nameserver 127.0.0.1' > \""+resolvConf+"\"; }\n")

	env := []string{
		"PATH=" + sandboxStubDir(t, logDir) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/root",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_UID=1000",
		"ENCLAVE_SANDBOX_GID=1000",
		"ENCLAVE_SANDBOX_ETC=" + etcDir,
		"ENCLAVE_SANDBOX_RUN_DIR=" + filepath.Join(t.TempDir(), "run"),
		"ENCLAVE_SANDBOX_RESOLV_CONF=" + resolvConf,
		"ENCLAVE_NET_LIB=" + netLib,
		"ENCLAVE_DNS_GATEWAY=1",
	}
	out, code := runSandboxEntrypoint(t, env)
	if code != 42 {
		t.Fatalf("expected launcher to fork the sandbox (stub exit 42), got %d\noutput:\n%s", code, out)
	}
	assertFileContent(t, resolvConf, "nameserver 127.0.0.1")
}

// TestEntrypointSandboxFailsClosedWhenResolverCannotBeSet asserts the launcher
// refuses to start a restricted session it cannot enforce DNS for, rather than
// degrading to the daemon resolver.
func TestEntrypointSandboxFailsClosedWhenResolverCannotBeSet(t *testing.T) {
	logDir := t.TempDir()
	etcDir := t.TempDir()
	writeFile(t, filepath.Join(etcDir, "passwd"), "agent:x:0:0::/home/agent:/bin/bash\n")
	writeFile(t, filepath.Join(etcDir, "group"), "agent:x:0:\n")
	netLib := filepath.Join(t.TempDir(), "net.sh")
	// Helper present but ineffective, mimicking a read-only resolv.conf.
	writeFile(t, netLib, "enclave_ensure_local_resolver() { return 0; }\n")

	env := []string{
		"PATH=" + sandboxStubDir(t, logDir) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/root",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_UID=1000",
		"ENCLAVE_SANDBOX_GID=1000",
		"ENCLAVE_SANDBOX_ETC=" + etcDir,
		"ENCLAVE_SANDBOX_RUN_DIR=" + filepath.Join(t.TempDir(), "run"),
		"ENCLAVE_SANDBOX_RESOLV_CONF=" + filepath.Join(t.TempDir(), "missing-resolv.conf"),
		"ENCLAVE_NET_LIB=" + netLib,
		"ENCLAVE_DNS_GATEWAY=1",
	}
	out, code := runSandboxEntrypoint(t, env)
	if code == 0 {
		t.Fatalf("expected launcher to fail closed without DNS enforcement\noutput:\n%s", out)
	}
	if !strings.Contains(out, "DNS enforcement bypassed") {
		t.Fatalf("unexpected error output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(logDir, "unshare.args")); err == nil {
		t.Fatalf("sandbox must not be forked when DNS enforcement cannot be applied")
	}
}

// TestEntrypointSandboxSealsSystemSubmounts guards the second half of the same
// bypass: a bind remount only affects the mount it names, so Docker's
// per-file /etc mounts (resolv.conf, hosts, hostname) and the /etc/sudoers.d
// tmpfs stayed writable and the agent could repoint DNS itself.
func TestEntrypointSandboxSealsSystemSubmounts(t *testing.T) {
	logDir := t.TempDir()
	dir := t.TempDir()
	for _, name := range []string{"mount", "setpriv"} {
		exitCode := "0"
		if name == "setpriv" {
			exitCode = "43"
		}
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + filepath.Join(logDir, name+".args") + "\nexit " + exitCode + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	// mountinfo field 5 is the mount point; only the /etc and /usr entries below
	// the sealed roots must be remounted, and /var/tmp must stay writable.
	mountinfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountinfo, strings.Join([]string{
		"1 0 0:1 / / rw - overlay overlay rw",
		"2 1 0:2 / /etc/resolv.conf rw - ext4 /dev/sda rw",
		"3 1 0:3 / /etc/hosts rw - ext4 /dev/sda rw",
		"4 1 0:4 / /etc/hostname rw - ext4 /dev/sda rw",
		"5 1 0:5 / /etc/sudoers.d rw - tmpfs tmpfs rw",
		"6 1 0:6 / /usr/share/nested rw - ext4 /dev/sda rw",
		"7 1 0:7 / /var/tmp rw - tmpfs tmpfs rw",
		"8 1 0:8 / /home/agent/.claude rw - ext4 /dev/sda rw",
		"",
	}, "\n"))

	env := []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/home/agent",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_PHASE=inner",
		"ENCLAVE_SANDBOX_MOUNTINFO=" + mountinfo,
	}
	out, code := runSandboxEntrypoint(t, env)
	if code != 43 {
		t.Fatalf("expected inner phase to exec setpriv (stub exit 43), got %d\noutput:\n%s", code, out)
	}
	args, err := os.ReadFile(filepath.Join(logDir, "mount.args"))
	if err != nil {
		t.Fatalf("mount not invoked: %v", err)
	}
	for _, want := range []string{
		"-o remount,bind,ro /etc/resolv.conf",
		"-o remount,bind,ro /etc/hosts",
		"-o remount,bind,ro /etc/hostname",
		"-o remount,bind,ro /etc/sudoers.d",
		"-o remount,bind,ro /usr/share/nested",
	} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("expected submount seal %q:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "-o remount,bind,ro /var/tmp\n") {
		t.Fatalf("/var/tmp must stay writable:\n%s", args)
	}
	// Session mounts outside the sealed roots must not be touched.
	if strings.Contains(string(args), "/home/agent/.claude") {
		t.Fatalf("store mounts must not be sealed:\n%s", args)
	}
}

func TestEntrypointSandboxInnerPhaseMountsReadOnlyAndDropsPrivileges(t *testing.T) {
	logDir := t.TempDir()
	dir := t.TempDir()
	stubs := map[string]string{
		"mount": `#!/bin/sh
printf '%s\n' "$*" >> ` + filepath.Join(logDir, "mount.args") + `
exit 0
`,
		"setpriv": `#!/bin/sh
printf '%s\n' "$*" >> ` + filepath.Join(logDir, "setpriv.args") + `
exit 43
`,
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	env := []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=/home/agent",
		"ENCLAVE_ROOTLESS_SANDBOX=1",
		"ENCLAVE_SANDBOX_PHASE=inner",
	}
	out, code := runSandboxEntrypoint(t, env)
	if code != 43 {
		t.Fatalf("expected inner phase to exec setpriv (stub exit 43), got %d\noutput:\n%s", code, out)
	}
	mountArgs, err := os.ReadFile(filepath.Join(logDir, "mount.args"))
	if err != nil {
		t.Fatalf("mount not invoked: %v", err)
	}
	for _, want := range []string{"--rbind /usr /usr", "-o remount,bind,ro /usr", "--rbind /etc /etc"} {
		if !strings.Contains(string(mountArgs), want) {
			t.Fatalf("mount args missing %q:\n%s", want, mountArgs)
		}
	}
	setprivArgs, err := os.ReadFile(filepath.Join(logDir, "setpriv.args"))
	if err != nil {
		t.Fatalf("setpriv not invoked: %v", err)
	}
	for _, want := range []string{"--ambient-caps -all", "--bounding-set -all", "--no-new-privs"} {
		if !strings.Contains(string(setprivArgs), want) {
			t.Fatalf("setpriv args missing %q:\n%s", want, setprivArgs)
		}
	}
}
