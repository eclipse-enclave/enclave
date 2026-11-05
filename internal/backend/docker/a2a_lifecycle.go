// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/peergroup"
)

// Defaults for the bounds A12 turns into options (proposal 17, P1 measurements).
const (
	DefaultA2ACommitTimeout       = 5 * time.Second
	DefaultA2ADepartureAckTimeout = 30 * time.Second
)

// PeerGroupConfig is the host-level configuration of peer group lifecycles.
type PeerGroupConfig struct {
	// HubImage runs the hub and the secrets ownership helper.
	HubImage string
	// HubEnv carries group-level hub settings (log mode, retained results,
	// reconnect window, max request bytes); taken from the member that
	// creates the hub. Port, uid and mount paths are fixed by the backend.
	HubEnv []backend.EnvVar
	// CommitTimeout bounds every snapshot acknowledgment wait.
	CommitTimeout time.Duration
	// DepartureAckTimeout bounds each worker's departure acknowledgment.
	DepartureAckTimeout time.Duration
}

// a2aEngine is the engine surface the lifecycle drives; *Backend implements
// it and tests substitute a fake.
type a2aEngine interface {
	backend.PeerGroupBackend
	inspectHub(ctx context.Context, group string) (hubState, error)
	restartHub(ctx context.Context, spec backend.HubSpec, state hubState) error
	// removeHub force-removes the hub, attachments included (rollback, teardown).
	removeHub(ctx context.Context, ref backend.HubRef) error
	usedAddresses(ctx context.Context, network, exclude string) (map[netip.Addr]string, error)
	// memberSession reports whether a session container exists and its labels.
	memberSession(ctx context.Context, container string) (memberSessionState, error)
	networkExists(ctx context.Context, network string) (bool, error)
}

type memberSessionState struct {
	Exists  bool
	Running bool
	// Ended: the container ran and stopped; a created or paused one has not ended.
	Ended  bool
	Labels map[string]string
}

// endedContainerStatus lists the inspect states of a container whose process is gone.
var endedContainerStatus = map[string]bool{"exited": true, "dead": true, "stopped": true}

func (b *Backend) removeHub(ctx context.Context, ref backend.HubRef) error {
	return removeHubContainer(ctx, hubRefName(ref))
}

func (b *Backend) usedAddresses(ctx context.Context, network, exclude string) (map[netip.Addr]string, error) {
	return usedNetworkAddresses(ctx, network, exclude)
}

func (b *Backend) memberSession(ctx context.Context, container string) (memberSessionState, error) {
	info, err := dockercmd.ContainerInspect(ctx, container)
	if err != nil {
		if dockercmd.IsNotFound(err) {
			return memberSessionState{}, nil
		}
		return memberSessionState{}, err
	}
	state := memberSessionState{Exists: true}
	if info.State != nil {
		state.Running = info.State.Running
		state.Ended = !info.State.Running && endedContainerStatus[strings.ToLower(info.State.Status)]
	}
	if info.Config != nil {
		state.Labels = info.Config.Labels
	}
	return state, nil
}

func (b *Backend) networkExists(ctx context.Context, network string) (bool, error) {
	if _, err := networkInspect(ctx, network); err != nil {
		if dockercmd.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// PeerGroups drives join, leave and reconcile of --peer-group sessions on
// this backend. Every mutation runs under the group lock (proposal 15).
type PeerGroups struct {
	engine a2aEngine
	// engineID is what group records created here carry; reconcile and the
	// lifecycle refuse groups of another engine.
	engineID  peergroup.Engine
	home      string
	stateRoot string
	cfg       PeerGroupConfig

	store     peergroup.GroupStore
	publisher peergroup.SnapshotPublisher
	waiter    peergroup.StatusWaiter
	inventory peergroup.Inventory
	tokens    peergroup.Tokens
	guard     peergroup.WorktreeGuard
	now       func() time.Time
}

// PeerGroups returns the lifecycle driver bound to this backend. It reads the
// engine info to identify the engine in group records and to choose how
// secrets.json is handed to the hub proxy uid.
func (b *Backend) PeerGroups(ctx context.Context, cfg PeerGroupConfig) (*PeerGroups, error) {
	info, err := dockerInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("read %s info: %w", dockercmd.Binary(), err)
	}
	owner, err := secretsOwnerFromInfo(info, cfg.HubImage)
	if err != nil {
		return nil, err
	}
	home := b.opts.Host.Home
	stateRoot := config.HostStateRootDir(home)
	p := newPeerGroups(b, home, stateRoot, cfg, peergroup.NewSnapshotPublisher(stateRoot, owner))
	p.store = peergroup.NewGroupStore(stateRoot, config.HostLocksDir(home))
	// The CLI wrapper, not the backend name, decides which engine is driven.
	name := backend.NameDocker
	if dockercmd.IsPodman() {
		name = backend.NamePodman
	}
	p.engineID = peergroup.Engine{Name: name, Root: info.DockerRootDir}
	return p, nil
}

func newPeerGroups(engine a2aEngine, home, stateRoot string, cfg PeerGroupConfig, publisher peergroup.SnapshotPublisher) *PeerGroups {
	if cfg.CommitTimeout <= 0 {
		cfg.CommitTimeout = DefaultA2ACommitTimeout
	}
	if cfg.DepartureAckTimeout <= 0 {
		cfg.DepartureAckTimeout = DefaultA2ADepartureAckTimeout
	}
	return &PeerGroups{
		engine:    engine,
		home:      home,
		stateRoot: stateRoot,
		cfg:       cfg,
		store:     peergroup.NewGroupStore(stateRoot, filepath.Join(stateRoot, "locks")),
		publisher: publisher,
		waiter:    peergroup.NewStatusWaiter(stateRoot),
		inventory: peergroup.NewInventory(stateRoot),
		tokens:    peergroup.NewTokens(stateRoot),
		guard:     peergroup.NewWorktreeGuard(),
		now:       time.Now,
	}
}

func (p *PeerGroups) hubSpec(rec *peergroup.GroupRecord) backend.HubSpec {
	return backend.HubSpec{
		Group:           rec.Group,
		GroupGeneration: string(rec.GroupGeneration),
		Image:           p.cfg.HubImage,
		SnapshotDir:     peergroup.SnapshotsDir(p.stateRoot, rec.Group),
		StatusDir:       peergroup.StatusDir(p.stateRoot, rec.Group),
		AuditDir:        peergroup.AuditDir(p.stateRoot, rec.Group),
		Env:             append([]backend.EnvVar(nil), p.cfg.HubEnv...),
	}
}

func hubRefFor(rec *peergroup.GroupRecord) backend.HubRef {
	name := rec.HubContainer
	if name == "" {
		name = HubContainerName(rec.Group)
	}
	return backend.HubRef{Group: rec.Group, Name: name}
}

func (p *PeerGroups) timestamp() string { return peergroup.FormatTimestamp(p.now()) }

// skillsKey identifies a member's published skills across revisions.
func skillsKey(member string, generation peergroup.Generation) string {
	return member + "\x00" + string(generation)
}

// currentSkills reads the skills of the published membership, since the
// group record does not keep them and the secrets file may be unreadable.
func (p *PeerGroups) currentSkills(group string) map[string][]a2a.AgentSkill {
	skills := map[string][]a2a.AgentSkill{}
	data, err := os.ReadFile(filepath.Join(peergroup.CurrentLinkPath(p.stateRoot, group), peergroup.MembershipFile))
	if err != nil {
		return skills
	}
	var m peergroup.Membership
	if err := json.Unmarshal(data, &m); err != nil {
		return skills
	}
	for _, member := range m.Members {
		skills[skillsKey(member.Name, member.Generation)] = member.Skills
	}
	return skills
}

// publishedMember reports whether rec publishes m: active members, plus the
// member a running join is admitting.
func publishedMember(m peergroup.GroupMember, joining string) bool {
	return m.State == peergroup.MemberActive || (m.State == peergroup.MemberJoining && m.Name == joining)
}

func (p *PeerGroups) snapshotFor(rec *peergroup.GroupRecord, joining string, extraSkills map[string][]a2a.AgentSkill) (peergroup.Membership, peergroup.Secrets, error) {
	skills := p.currentSkills(rec.Group)
	for key, value := range extraSkills {
		skills[key] = value
	}
	outbound := map[string]peergroup.OutboundSecret{}
	if stored, err := p.tokens.LoadOutbound(rec.Group); err == nil {
		for _, secret := range stored.Outbound {
			outbound[skillsKey(secret.Member, secret.Generation)] = secret
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return peergroup.Membership{}, peergroup.Secrets{}, fmt.Errorf("read host copy of outbound tokens: %w", err)
	}
	m := peergroup.Membership{
		SchemaVersion:   peergroup.SchemaVersion,
		Group:           rec.Group,
		GroupGeneration: rec.GroupGeneration,
		HubPort:         peergroup.HubPort,
		HostPrincipal: peergroup.HostPrincipalEntry{
			Name:        rec.HostPrincipal.Name,
			Generation:  rec.HostPrincipal.Generation,
			Credentials: rec.CredentialHashes(p.now()),
		},
	}
	s := peergroup.Secrets{SchemaVersion: peergroup.SchemaVersion, Group: rec.Group, GroupGeneration: rec.GroupGeneration}
	for _, member := range rec.Members {
		if !publishedMember(member, joining) {
			continue
		}
		key := skillsKey(member.Name, member.Generation)
		m.Members = append(m.Members, peergroup.MembershipMember{
			Name:             member.Name,
			Role:             member.Role,
			Generation:       member.Generation,
			ProjectPath:      member.ProjectPath,
			MountMode:        member.MountMode,
			Network:          member.Network,
			Subnet:           member.Subnet,
			HubAddress:       member.HubAddress,
			GatewayAddress:   member.GatewayAddress,
			AdapterPort:      member.AdapterPort,
			InboundTokenHash: member.InboundTokenHash,
			Description:      member.Description,
			Tool:             member.Tool,
			Skills:           skills[key],
		})
		if member.Role != a2a.RoleWorker {
			continue
		}
		secret, ok := outbound[key]
		if !ok {
			return peergroup.Membership{}, peergroup.Secrets{}, fmt.Errorf("no outbound token on record for worker %s", member.Name)
		}
		s.Outbound = append(s.Outbound, secret)
	}
	return m, s, nil
}

// commitRevision publishes rec's membership and waits for both readers
// (proposal 6); on success rec carries the new committed revision.
func (p *PeerGroups) commitRevision(ctx context.Context, rec *peergroup.GroupRecord, purpose, member, joining string, extraSkills map[string][]a2a.AgentSkill) error {
	rev, err := p.publishRevision(ctx, rec, purpose, member, joining, extraSkills)
	if err != nil {
		return err
	}
	return p.awaitRevision(ctx, rec, rev)
}

func addObligation(rec *peergroup.GroupRecord, ob peergroup.Obligation) {
	for i, existing := range rec.Obligations {
		if existing.Kind == ob.Kind && existing.Member == ob.Member && existing.Generation == ob.Generation {
			rec.Obligations[i].LastAttemptAt = ob.LastAttemptAt
			rec.Obligations[i].LastError = ob.LastError
			if ob.Revision != 0 {
				rec.Obligations[i].Revision = ob.Revision
			}
			return
		}
	}
	rec.Obligations = append(rec.Obligations, ob)
}

func removeObligation(rec *peergroup.GroupRecord, kind, member string, generation peergroup.Generation) {
	kept := rec.Obligations[:0:0]
	for _, ob := range rec.Obligations {
		if ob.Kind == kind && ob.Member == member && ob.Generation == generation {
			continue
		}
		kept = append(kept, ob)
	}
	rec.Obligations = kept
}

func findMember(rec *peergroup.GroupRecord, name string) int {
	for i, m := range rec.Members {
		if m.Name == name {
			return i
		}
	}
	return -1
}

func removeMember(rec *peergroup.GroupRecord, name string, generation peergroup.Generation) {
	kept := rec.Members[:0:0]
	for _, m := range rec.Members {
		if m.Name == name && m.Generation == generation {
			continue
		}
		kept = append(kept, m)
	}
	rec.Members = kept
}

// dropOutbound removes a worker's token from the host copy.
func (p *PeerGroups) dropOutbound(group, member string, generation peergroup.Generation) error {
	stored, err := p.tokens.LoadOutbound(group)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	kept := stored.Outbound[:0:0]
	for _, secret := range stored.Outbound {
		if secret.Member == member && secret.Generation == generation {
			continue
		}
		kept = append(kept, secret)
	}
	stored.Outbound = kept
	return p.tokens.SaveOutbound(group, *stored)
}

func (p *PeerGroups) addOutbound(rec *peergroup.GroupRecord, member string, generation peergroup.Generation, token string) error {
	stored, err := p.tokens.LoadOutbound(rec.Group)
	if errors.Is(err, fs.ErrNotExist) {
		stored = &peergroup.Secrets{SchemaVersion: peergroup.SchemaVersion, Group: rec.Group, GroupGeneration: rec.GroupGeneration}
	} else if err != nil {
		return err
	}
	stored.GroupGeneration = rec.GroupGeneration
	stored.Outbound = append(stored.Outbound, peergroup.OutboundSecret{Member: member, Generation: generation, Token: token})
	return p.tokens.SaveOutbound(rec.Group, *stored)
}

// overlayGeneration returns the generation of an existing overlay file, 0 if none.
func overlayGeneration(path string) uint64 {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0
	}
	var o peergroup.Overlay
	if json.Unmarshal(data, &o) != nil {
		return 0
	}
	return o.Generation
}

func (p *PeerGroups) overlayPath(m peergroup.GroupMember) string {
	return peergroup.OverlayPath(config.HostProjectToolDir(p.home, m.ProjectHash, m.Tool), m.SessionContainer)
}

// ensureOverlayDir forces 0755 past the umask: a gateway under userns-remap
// is neither the owner nor root on the host, so it reads through the other bits.
func ensureOverlayDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- the gateway must read the overlay through the other bits.
		return err
	}
	return os.Chmod(dir, 0o755) // #nosec G302 -- as above.
}

// clearOverlay writes an inactive overlay, waits for the gateway to clear the
// rules when it runs, then deletes the session's overlay directory.
func (p *PeerGroups) clearOverlay(ctx context.Context, group string, m peergroup.GroupMember, gatewayRunning bool) error {
	path := p.overlayPath(m)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if gatewayRunning {
		generation := overlayGeneration(path) + 1
		if err := peergroup.WriteOverlay(path, peergroup.Overlay{
			SchemaVersion: peergroup.SchemaVersion,
			Generation:    generation,
			Active:        false,
			Group:         group,
			Member:        m.Name,
		}); err != nil {
			return err
		}
		if err := p.engine.ReloadGatewayOverlay(ctx, backend.SessionRef{Name: m.SessionContainer}, generation); err != nil {
			return fmt.Errorf("clear A2A overlay of %s: %w", m.Name, err)
		}
	}
	return os.RemoveAll(filepath.Dir(path))
}

// lockGroup is the group lock; tests replace it to observe ordering.
func (p *PeerGroups) lockGroup(ctx context.Context, group string) (func(), error) {
	if err := peergroup.ValidateGroupName(group); err != nil {
		return nil, err
	}
	unlock, err := p.store.Lock(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("lock peer group %s: %w", group, err)
	}
	var once sync.Once
	return func() { once.Do(unlock) }, nil
}
