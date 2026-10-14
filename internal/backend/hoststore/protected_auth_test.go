// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"enclave/internal/backend"
)

func protectedIdentityTestDir(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	dir, err := ResolveDir(home, backend.StoreKey{Owner: "claude", Suffix: "fresh"}, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func identityMetadataTestDir(t *testing.T) (string, string) {
	t.Helper()
	home, dir := protectedIdentityTestDir(t)
	if err := os.MkdirAll(filepath.Dir(IdentityStatePath(home, dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func unusedIdentity() (bool, error) { return true, nil }

func testIdentityOwner(engine, session string, protected bool) IdentityReservation {
	return IdentityReservation{Engine: engine, Session: session, Incarnation: strings.Repeat(session, 32), Protected: protected}
}

func TestProtectedIdentityConcurrentAdmissionAndVerifiedRelease(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := testIdentityOwner(backend.NameDocker, string(rune('a'+i)), true)
			if i%2 != 0 {
				owner.Engine = backend.NamePodman
			}
			if err := ReserveIdentity(home, dir, owner); err == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 12 {
		t.Fatalf("admitted %d concurrent owners, want twelve", admitted.Load())
	}
	state, err := readIdentityState(home, dir)
	if err != nil || len(state.Reservations) != 12 {
		t.Fatalf("durable reservations = %+v, %v", state, err)
	}
	winner := state.Reservations[0]
	// Opening the state again (as after detached CLI exit or proxy reload)
	// leaves admission fenced. A different incarnation cannot release it.
	wrong := winner
	wrong.Incarnation = strings.Repeat("z", 32)
	if err := ReleaseIdentity(home, dir, wrong, func(IdentityReservation) (bool, error) { t.Fatal("verified wrong incarnation"); return true, nil }); err != nil {
		t.Fatal(err)
	}
	for _, verify := range []func(IdentityReservation) (bool, error){
		nil,
		func(IdentityReservation) (bool, error) { return false, nil },
		func(IdentityReservation) (bool, error) { return false, errors.New("engine unavailable") },
	} {
		if err := ReleaseIdentity(home, dir, winner, verify); err == nil {
			t.Fatal("released owner without verified session end")
		}
	}
	if err := RequireIdentityReservation(home, dir, winner); err != nil {
		t.Fatalf("reservation lost across failed verification: %v", err)
	}
	if err := ReserveIdentity(home, dir, winner); err == nil {
		t.Fatal("duplicate session admitted")
	}
	if err := ReleaseIdentity(home, dir, winner, func(IdentityReservation) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := RequireIdentityReservation(home, dir, winner); err == nil {
		t.Fatal("ended reservation survived")
	}
	for _, sibling := range state.Reservations[1:] {
		if err := RequireIdentityReservation(home, dir, sibling); err != nil {
			t.Fatalf("sibling reservation lost: %v", err)
		}
	}
	if err := ReserveIdentity(home, dir, testIdentityOwner(backend.NamePodman, "n", true)); err != nil {
		t.Fatalf("new logical session: %v", err)
	}
}

func TestLegacyReservationsRemainConcurrentButBlockProtection(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	first := testIdentityOwner(backend.NameDocker, "a", false)
	second := testIdentityOwner(backend.NamePodman, "b", false)
	for _, owner := range []IdentityReservation{first, second} {
		if err := ReserveIdentity(home, dir, owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := RequireUnprotectedAuth(home, dir); err != nil {
		t.Fatal(err)
	}
	if err := ProtectIdentity(home, dir, unusedIdentity); err == nil {
		t.Fatal("protected a live legacy identity")
	}
	for _, owner := range []IdentityReservation{first, second} {
		if err := ReleaseIdentity(home, dir, owner, func(IdentityReservation) (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	if err := ReserveIdentity(home, dir, first); err == nil {
		t.Fatal("admitted legacy access to a protected identity")
	}
}

func TestProtectionRequiresFreshUnusedIdentity(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	if err := ProtectIdentity(home, dir, nil); err == nil {
		t.Fatal("missing cross-engine verification accepted")
	}
	if err := ProtectIdentity(home, dir, func() (bool, error) { return false, nil }); err == nil {
		t.Fatal("live legacy session accepted")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credentialPath, []byte("existing-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectIdentity(home, dir, unusedIdentity); err == nil {
		t.Fatal("existing credentials silently enrolled")
	}
	data, err := os.ReadFile(credentialPath)
	if err != nil || string(data) != "existing-credential" {
		t.Fatalf("existing credentials modified: %q, %v", data, err)
	}
}

func TestResetRetainsProtectionAndDowngradeIsExplicit(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	grantPath := filepath.Join(dir, "oauth-grant.json")
	if err := os.WriteFile(grantPath, []byte("trusted-grant"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DowngradeIdentity(home, dir, unusedIdentity); err == nil {
		t.Fatal("downgrade retained a grant that could be mounted raw")
	}
	if err := WithIdleProtectedIdentity(home, dir, unusedIdentity, func() error { return os.Remove(grantPath) }); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err == nil {
		t.Fatal("reset silently downgraded protection")
	}
	if err := DowngradeIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(IdentityStatePath(home, dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity metadata permissions: %v, %v", info, err)
	}
}

func TestProtectedAuthMetadataFailsClosed(t *testing.T) {
	for _, data := range []string{
		"null", "{}", `{"schemaVersion":1}`,
		`{"schemaVersion":null,"protected":false}`,
		`{"schemaVersion":2,"protected":false}`,
		`{"schemaVersion":1,"protected":true} {}`,
		`{"schemaVersion":1,"protected":false,"unexpected":true}`,
		`{"schemaVersion":1,"protected":true,"protected":false}`,
		`{"schemaVersion":1,"Protected":false}`,
		`{"schemaVersion":1,"protected":false,"reservations":null}`,
	} {
		t.Run(data, func(t *testing.T) {
			home, dir := identityMetadataTestDir(t)
			if err := os.WriteFile(IdentityStatePath(home, dir), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := RequireUnprotectedAuth(home, dir); err == nil {
				t.Fatal("corrupt metadata allowed raw credential access")
			}
		})
	}
	home, dir := identityMetadataTestDir(t)
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), IdentityStatePath(home, dir)); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err == nil {
		t.Fatal("symlinked marker allowed legacy access")
	}
}

func TestRequireUnprotectedAuthRejectsUnsafeMetadataPermissions(t *testing.T) {
	home, dir := identityMetadataTestDir(t)
	file := IdentityStatePath(home, dir)
	if err := os.WriteFile(file, []byte(`{"schemaVersion":1,"protected":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err != nil {
		t.Fatalf("valid private legacy marker rejected: %v", err)
	}
	for _, mode := range []os.FileMode{0o644, 0o660, 0o601} {
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		if err := RequireUnprotectedAuth(home, dir); err == nil {
			t.Fatalf("marker mode %o allowed legacy access", mode)
		}
	}
}

func TestRequireUnprotectedAuthRejectsSymlinkToValidLegacyMarker(t *testing.T) {
	home, dir := identityMetadataTestDir(t)
	target := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(target, []byte(`{"schemaVersion":1,"protected":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, IdentityStatePath(home, dir)); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err == nil {
		t.Fatal("valid contents of a symlink target bypassed the marker guard")
	}
}

func TestProtectedIdentityCannotTraverseSymlinkedStoreParent(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(filepath.Dir(parent), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), parent); err != nil {
		t.Fatal(err)
	}
	if err := ProtectIdentity(home, dir, unusedIdentity); err == nil {
		t.Fatal("identity mutation followed a symlinked store parent")
	}
}

func TestWorkloadFilesCannotEnrollOrCorruptIdentityProtection(t *testing.T) {
	for _, data := range []string{`{"schemaVersion":1,"protected":true}`, "corrupt metadata"} {
		t.Run(data, func(t *testing.T) {
			home, dir := protectedIdentityTestDir(t)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".enclave-oauth-identity.json"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := RequireUnprotectedAuth(home, dir); err != nil {
				t.Fatalf("workload-written file blocked legacy access: %v", err)
			}
			owner := testIdentityOwner(backend.NameDocker, "a", false)
			if err := ReserveIdentity(home, dir, owner); err != nil {
				t.Fatalf("workload-written file blocked admission: %v", err)
			}
			state, err := readIdentityState(home, dir)
			if err != nil || state.Protected || len(state.Reservations) != 1 {
				t.Fatalf("host-only admission metadata = %+v, %v", state, err)
			}
		})
	}
}

func TestRemovingAuthStoreCannotClearProtection(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("protection metadata leaked into workload mount: %v, %v", entries, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err == nil {
		t.Fatal("removing auth store cleared host-only protection")
	}
}

func TestIdentityMetadataRejectsSymlinkedParent(t *testing.T) {
	home, dir := protectedIdentityTestDir(t)
	parent := filepath.Dir(IdentityStatePath(home, dir))
	if err := os.MkdirAll(filepath.Dir(parent), 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, parent); err != nil {
		t.Fatal(err)
	}
	if err := RequireUnprotectedAuth(home, dir); err == nil {
		t.Fatal("legacy guard followed a symlinked metadata directory")
	}
	if err := ProtectIdentity(home, dir, unusedIdentity); err == nil {
		t.Fatal("protection followed a symlinked metadata directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote lock or metadata through a symlink: %v, %v", entries, err)
	}
}

func TestIdentityBookkeepingDoesNotRecreateOrChmodAuthDirectory(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name := "existing directory"
		if missing {
			name = "removed directory"
		}
		t.Run(name, func(t *testing.T) {
			home, dir := protectedIdentityTestDir(t)
			if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
				t.Fatal(err)
			}
			if missing {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			owner := testIdentityOwner(backend.NameDocker, "a", true)
			steps := []struct {
				name string
				run  func() error
			}{
				{"reserve", func() error { return ReserveIdentity(home, dir, owner) }},
				{"release", func() error {
					return ReleaseIdentity(home, dir, owner, func(IdentityReservation) (bool, error) { return true, nil })
				}},
				{"downgrade", func() error { return DowngradeIdentity(home, dir, unusedIdentity) }},
			}
			for _, step := range steps {
				if err := step.run(); err != nil {
					t.Fatalf("%s: %v", step.name, err)
				}
				info, err := os.Stat(dir)
				if missing {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("%s recreated auth directory: %v", step.name, err)
					}
				} else if err != nil || info.Mode().Perm() != 0o750 {
					t.Fatalf("%s changed auth directory permissions: %v, %v", step.name, info, err)
				}
			}
			if err := RequireUnprotectedAuth(home, dir); err != nil {
				t.Fatalf("bookkeeping failed to persist downgrade: %v", err)
			}
		})
	}
}

// Protection follows the identity, not the spelling of its path: an aliased
// state root or another case of the same directory finds the same metadata.
func TestIdentityProtectionSurvivesPathAliases(t *testing.T) {
	state := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state-alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", state)
	home := t.TempDir()
	dir, err := ResolveDir(home, backend.StoreKey{Owner: "claude", Suffix: "fresh"}, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	if err := ProtectIdentity(home, dir, unusedIdentity); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", alias)
	aliased, err := ResolveDir(home, backend.StoreKey{Owner: "claude", Suffix: "fresh"}, backend.StoreKindAuth)
	if err != nil {
		t.Fatal(err)
	}
	cased := strings.Replace(aliased, string(filepath.Separator)+"claude"+string(filepath.Separator), string(filepath.Separator)+"CLAUDE"+string(filepath.Separator), 1)
	for _, spelling := range []string{aliased, cased} {
		if protected, err := IdentityProtected(home, spelling); err != nil || !protected {
			t.Fatalf("%s: protected = %t, %v", spelling, protected, err)
		}
	}
}

// Builds before path-relative keys kept metadata under the raw path; reading
// it adopts that metadata rather than losing protection.
func TestIdentityProtectionAdoptsLegacyMetadata(t *testing.T) {
	home, dir := identityMetadataTestDir(t)
	legacy := legacyIdentityStatePath(home, dir)
	if err := os.WriteFile(legacy, []byte(`{"schemaVersion":1,"protected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if protected, err := IdentityProtected(home, dir); err != nil || !protected {
		t.Fatalf("legacy protection lost: %t, %v", protected, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy metadata not adopted: %v", err)
	}
	if _, err := os.Stat(IdentityStatePath(home, dir)); err != nil {
		t.Fatal(err)
	}
}

// A dangling link is not a missing directory: it may point at a grant that
// has not been written yet.
func TestCanonicalAuthPathRefusesDanglingLinks(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "source")
	if err := os.Symlink(filepath.Join(root, "future-grant"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalAuthPath(filepath.Join(link, "file")); err == nil {
		t.Fatal("dangling link accepted as an absent component")
	}
	if got, err := CanonicalAuthPath(filepath.Join(root, "absent", "file")); err != nil || got != filepath.Join(root, "absent", "file") {
		t.Fatalf("absent components = %q, %v", got, err)
	}
}
