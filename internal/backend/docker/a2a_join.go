// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/peergroup"
)

// JoinSpec describes a session joining a peer group (proposal 15 "Join").
type JoinSpec struct {
	Group string
	Role  a2a.Role
	// Member is the sanitized session name; SessionContainer the engine name.
	Member           string
	SessionContainer string
	Tool             string
	ProjectHash      string
	ConfigStoreKey   string
	// ConfigStoreDir is config.HostStoreConfigDir of the session; the
	// context table lives below it per member generation.
	ConfigStoreDir string
	// ContainerConfigDir is the tool's config directory inside the container.
	ContainerConfigDir string
	// ProjectPath is host-canonical; MountMode is peergroup.MountReadWrite or MountReadOnly.
	ProjectPath string
	MountMode   string
	Description string
	Skills      []a2a.AgentSkill
	// SessionEnv carries the bound variables of interfaces.md section 6.
	SessionEnv []backend.EnvVar
}

// Join is a join in progress. It holds the group lock from Join until the
// session container is created (or the join is rolled back).
type Join struct {
	p      *PeerGroups
	spec   JoinSpec
	unlock func()
	steps  a2aSteps

	rec          *peergroup.GroupRecord
	createdGroup bool
	generation   peergroup.Generation
	creds        peergroup.MemberCredentials
	session      *backend.PeerGroupSession
	overlayPath  string
	hub          backend.HubRef
	warnings     []string

	mu       sync.Mutex
	finished bool
	leave    *LeaveResult
	// stopHold ends the lock hold Started may run in the background.
	stopHold func()
	holdOnce sync.Once
}

var _ backend.PeerGroupJoin = (*Join)(nil)

func joinFor(session *backend.PeerGroupSession) (backend.PeerGroupJoin, error) {
	if session == nil {
		return nil, nil
	}
	if session.Join == nil {
		return nil, fmt.Errorf("peer group session %s/%s was not prepared by PeerGroups.Join", session.Group, session.Member)
	}
	return session.Join, nil
}

// errJoinFinished: a join completes one session start only.
var errJoinFinished = errors.New("peer group join already finished; prepare a new one")

func (j *Join) checkActive() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return errJoinFinished
	}
	return nil
}

func (s JoinSpec) validate() error {
	if err := peergroup.ValidateGroupName(s.Group); err != nil {
		return err
	}
	if s.Role != a2a.RoleOrchestrator && s.Role != a2a.RoleWorker {
		return fmt.Errorf("peer group role must be %s or %s, got %q", a2a.RoleOrchestrator, a2a.RoleWorker, s.Role)
	}
	if s.Member == "" || s.Member == peergroup.HostMemberName || s.SessionContainer == "" {
		return fmt.Errorf("peer group member needs a session name and container")
	}
	if s.Tool == "" || s.ProjectHash == "" || !filepath.IsAbs(s.ConfigStoreDir) {
		return fmt.Errorf("peer group member %s needs a tool, project hash and absolute config store", s.Member)
	}
	if s.MountMode != peergroup.MountReadWrite && s.MountMode != peergroup.MountReadOnly {
		return fmt.Errorf("peer group member %s has mount mode %q", s.Member, s.MountMode)
	}
	if s.Role == a2a.RoleWorker && s.ContainerConfigDir == "" {
		return fmt.Errorf("peer group worker %s needs its container config directory", s.Member)
	}
	return nil
}

// Join runs the host-only join steps (record, inventory, tokens, worktree
// guard) under the group lock and returns the join whose Session goes into
// backend.Request.PeerGroup. Session start runs the remaining steps; Abort
// undoes everything when the caller fails before starting the session.
func (p *PeerGroups) Join(ctx context.Context, spec JoinSpec) (*Join, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	unlock, err := p.lockGroup(ctx, spec.Group)
	if err != nil {
		return nil, err
	}
	j := &Join{p: p, spec: spec, unlock: unlock}
	if err := j.steps.run(ctx, j.stepLoadGroup(), j.stepCheckMember(), j.stepInventory(), j.stepCredentials(), j.stepRecordMember()); err != nil {
		_ = j.Fail(ctx)
		return nil, fmt.Errorf("join peer group %s: %w", spec.Group, err)
	}
	j.session = j.buildSession()
	j.session.Join = j
	return j, nil
}

// Session is what the member's session start needs (backend.Request.PeerGroup).
func (j *Join) Session() *backend.PeerGroupSession { return j.session }

// Warnings are the worktree guard's non-fatal overlaps.
func (j *Join) Warnings() []string { return append([]string(nil), j.warnings...) }

// Generation is the member generation issued by this join.
func (j *Join) Generation() peergroup.Generation { return j.generation }

// LeaveResult is the automatic leave of a foreground session, nil before it ran.
func (j *Join) LeaveResult() *LeaveResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.leave
}

// Abort rolls back a join whose session never started.
func (j *Join) Abort(ctx context.Context) error {
	return j.Fail(ctx)
}

// Fail rolls back every completed step and releases the group lock.
func (j *Join) Fail(ctx context.Context) error {
	j.mu.Lock()
	if j.finished {
		j.mu.Unlock()
		return nil
	}
	j.finished = true
	j.mu.Unlock()
	err := j.steps.rollback(ctx)
	if err != nil {
		logx.Warnf("Rolling back the join of %s to peer group %s left work for `enclave a2a reconcile`: %v", j.spec.Member, j.spec.Group, err)
	}
	j.unlock()
	return err
}

// startedLockHold bounds how long Started keeps the group lock while the
// engine has not created the session container yet.
var startedLockHold = 30 * time.Second

// Started releases the group lock once the session container exists. A
// foreground start calls it before the engine creates the container, so the
// release waits for it: a reconcile in between would take the active member
// whose container is missing for a departure.
func (j *Join) Started() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	j.finished = true
	j.steps.done = nil
	if j.sessionCreated() {
		j.unlock()
		return
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	j.stopHold = func() {
		j.holdOnce.Do(func() { close(stop) })
		<-stopped
	}
	go func() {
		defer close(stopped)
		defer j.unlock()
		deadline := time.NewTimer(startedLockHold)
		defer deadline.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				if j.sessionCreated() {
					return
				}
			}
		}
	}()
}

// releaseStartedLock ends a lock hold of Started early, before the leave of
// a session whose container never appeared or already ended.
func (j *Join) releaseStartedLock() {
	j.mu.Lock()
	stop := j.stopHold
	j.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (j *Join) sessionCreated() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := j.p.engine.memberSession(ctx, j.spec.SessionContainer)
	return err == nil && session.Exists && session.Labels[peergroup.LabelMemberGeneration] == string(j.generation)
}

func (j *Join) memberIndex() int {
	for i, m := range j.rec.Members {
		if m.Name == j.spec.Member && m.Generation == j.generation {
			return i
		}
	}
	return -1
}

func (j *Join) member() *peergroup.GroupMember {
	if i := j.memberIndex(); i >= 0 {
		return &j.rec.Members[i]
	}
	return nil
}

func (j *Join) stepLoadGroup() a2aStep {
	return a2aStep{
		name: "load or create group record",
		do: func(ctx context.Context) error {
			p := j.p
			rec, err := p.store.Load(j.spec.Group)
			if err != nil && !errors.Is(err, peergroup.ErrGroupNotFound) {
				return err
			}
			if rec != nil {
				if err := p.claimEngine(ctx, rec); err != nil {
					return err
				}
			}
			hub, hubErr := p.engine.inspectHub(ctx, j.spec.Group)
			if hubErr != nil {
				return hubErr
			}
			if rec != nil {
				if hub.Exists && hub.GroupGeneration != string(rec.GroupGeneration) {
					if err := p.engine.removeHub(ctx, hub.Ref); err != nil {
						return fmt.Errorf("remove A2A hub of an earlier group generation: %w", err)
					}
				}
				j.rec = rec
				return nil
			}
			// Create resets snapshots and status, so the old hub must be gone first.
			if hub.Exists {
				if err := p.engine.removeHub(ctx, hub.Ref); err != nil {
					return fmt.Errorf("remove A2A hub left by an earlier group generation: %w", err)
				}
			}
			rec, err = p.store.CreateOn(j.spec.Group, p.engineID)
			if err != nil {
				return err
			}
			j.rec, j.createdGroup = rec, true
			return nil
		},
		undo: func(ctx context.Context) error {
			if !j.createdGroup {
				return nil
			}
			err := j.p.store.Delete(j.rec.Group, j.rec.GroupGeneration)
			if errors.Is(err, peergroup.ErrGroupNotFound) {
				return nil
			}
			return err
		},
	}
}

func (j *Join) stepCheckMember() a2aStep {
	return a2aStep{
		name: "check member and worktree guard",
		do: func(ctx context.Context) error {
			var existing []peergroup.GuardEntry
			for _, m := range j.rec.Members {
				if m.Name == j.spec.Member {
					return fmt.Errorf("peer group %s already has a member named %s (%s)", j.rec.Group, m.Name, m.State)
				}
				if j.spec.Role == a2a.RoleOrchestrator && m.Role == a2a.RoleOrchestrator {
					return fmt.Errorf("peer group %s already has orchestrator %s", j.rec.Group, m.Name)
				}
				existing = append(existing, peergroup.GuardEntry{Member: m.Name, ProjectPath: m.ProjectPath, MountMode: m.MountMode})
			}
			res, err := j.p.guard.Check(existing, peergroup.GuardEntry{Member: j.spec.Member, ProjectPath: j.spec.ProjectPath, MountMode: j.spec.MountMode})
			if err != nil {
				return err
			}
			j.warnings = res.Warnings
			return nil
		},
	}
}

func (j *Join) stepInventory() a2aStep {
	return a2aStep{
		name: "write cleanup inventory entry",
		do: func(ctx context.Context) error {
			gen, err := peergroup.NewGeneration()
			if err != nil {
				return err
			}
			j.generation = gen
			return j.p.inventory.Add(j.rec.Group, j.rec.GroupGeneration, peergroup.InventoryEntry{
				Member:           j.spec.Member,
				MemberGeneration: gen,
				Tool:             j.spec.Tool,
				ProjectHash:      j.spec.ProjectHash,
				ConfigStoreKey:   j.spec.ConfigStoreKey,
				StorePath:        peergroup.ContextTableDir(j.spec.ConfigStoreDir, gen),
			})
		},
		undo: func(ctx context.Context) error {
			return j.p.inventory.Remove(j.rec.Group, j.rec.GroupGeneration, j.spec.Member, j.generation)
		},
	}
}

func (j *Join) stepCredentials() a2aStep {
	return a2aStep{
		name: "issue member tokens",
		do: func(ctx context.Context) error {
			creds, err := j.p.tokens.NewMember(j.spec.Role)
			if err != nil {
				return err
			}
			j.creds = creds
			if j.spec.Role != a2a.RoleWorker {
				return nil
			}
			return j.p.addOutbound(j.rec, j.spec.Member, j.generation, creds.Outbound)
		},
		undo: func(ctx context.Context) error {
			if j.spec.Role != a2a.RoleWorker {
				return nil
			}
			return j.p.dropOutbound(j.rec.Group, j.spec.Member, j.generation)
		},
	}
}

func (j *Join) stepRecordMember() a2aStep {
	return a2aStep{
		name: "record joining member",
		do: func(ctx context.Context) error {
			adapterPort := 0
			if j.spec.Role == a2a.RoleWorker {
				adapterPort = peergroup.AdapterPort
			}
			j.rec.Members = append(j.rec.Members, peergroup.GroupMember{
				Name:             j.spec.Member,
				Role:             j.spec.Role,
				Generation:       j.generation,
				State:            peergroup.MemberJoining,
				SessionContainer: j.spec.SessionContainer,
				Tool:             j.spec.Tool,
				ProjectHash:      j.spec.ProjectHash,
				ConfigStoreKey:   j.spec.ConfigStoreKey,
				ProjectPath:      j.spec.ProjectPath,
				MountMode:        j.spec.MountMode,
				AdapterPort:      adapterPort,
				InboundTokenHash: j.creds.InboundHash,
				Description:      j.spec.Description,
			})
			return j.p.store.Save(j.rec)
		},
		undo: func(ctx context.Context) error {
			removeMember(j.rec, j.spec.Member, j.generation)
			return j.p.store.Save(j.rec)
		},
	}
}

func (j *Join) buildSession() *backend.PeerGroupSession {
	spec := j.spec
	j.overlayPath = peergroup.OverlayPath(config.HostProjectToolDir(j.p.home, spec.ProjectHash, spec.Tool), spec.SessionContainer)
	env := []backend.EnvVar{
		{Name: a2a.EnvRole, Value: string(spec.Role)},
		{Name: a2a.EnvGroup, Value: spec.Group},
		{Name: a2a.EnvGroupGeneration, Value: string(j.rec.GroupGeneration)},
		{Name: a2a.EnvMember, Value: spec.Member},
		{Name: a2a.EnvMemberGeneration, Value: string(j.generation)},
		{Name: a2a.EnvHubHost, Value: a2a.HubDNSName},
		{Name: a2a.EnvHubPort, Value: strconv.Itoa(peergroup.HubPort)},
		{Name: a2a.EnvInboundToken, Value: j.creds.Inbound},
	}
	if spec.Role == a2a.RoleWorker {
		env = append(env,
			backend.EnvVar{Name: a2a.EnvOutboundToken, Value: j.creds.Outbound},
			backend.EnvVar{Name: a2a.EnvAdapterPort, Value: strconv.Itoa(peergroup.AdapterPort)},
			backend.EnvVar{Name: a2a.EnvTableDir, Value: path.Join(spec.ContainerConfigDir, peergroup.ContextTableDirName, string(j.generation))},
			backend.EnvVar{Name: a2a.EnvDescription, Value: spec.Description},
		)
	}
	set := map[string]bool{}
	for _, entry := range env {
		set[entry.Name] = true
	}
	for _, entry := range spec.SessionEnv {
		if entry.Name != "" && !set[entry.Name] {
			env = append(env, entry)
		}
	}
	return &backend.PeerGroupSession{
		Group:            spec.Group,
		GroupGeneration:  string(j.rec.GroupGeneration),
		Member:           spec.Member,
		MemberGeneration: string(j.generation),
		Role:             spec.Role,
		OverlayDir:       filepath.Dir(j.overlayPath),
		Env:              env,
		Labels: map[string]string{
			peergroup.LabelGroup:            spec.Group,
			peergroup.LabelGroupGeneration:  string(j.rec.GroupGeneration),
			peergroup.LabelMember:           spec.Member,
			peergroup.LabelMemberGeneration: string(j.generation),
			peergroup.LabelRole:             string(spec.Role),
		},
	}
}

// beforeGateway runs once the session network exists and before the gateway
// is created: assign the hub address and write the overlay the gateway
// applies at start.
func (j *Join) beforeGateway(ctx context.Context, network, subnet string) error {
	return j.steps.run(ctx,
		a2aStep{
			name: "assign hub address",
			do: func(ctx context.Context) error {
				used, err := j.p.engine.usedAddresses(ctx, network, hubRefFor(j.rec).Name)
				if err != nil {
					return err
				}
				addr, err := hubAddressFor(subnet, used)
				if err != nil {
					return err
				}
				m := j.member()
				m.Network, m.Subnet, m.HubAddress = network, subnet, addr.String()
				return nil
			},
		},
		a2aStep{
			name: "write gateway overlay",
			do: func(ctx context.Context) error {
				m := j.member()
				if err := ensureOverlayDir(filepath.Dir(j.overlayPath)); err != nil {
					return err
				}
				return peergroup.WriteOverlay(j.overlayPath, peergroup.Overlay{
					SchemaVersion: peergroup.SchemaVersion,
					Generation:    overlayGeneration(j.overlayPath) + 1,
					Active:        true,
					Group:         j.rec.Group,
					Member:        m.Name,
					HubAddress:    m.HubAddress,
					HubPort:       peergroup.HubPort,
					AdapterPort:   m.AdapterPort,
					DNSName:       a2a.HubDNSName,
				})
			},
			undo: func(ctx context.Context) error {
				return os.RemoveAll(filepath.Dir(j.overlayPath))
			},
		},
	)
}

// afterGateway runs once the gateway is ready: attach the hub, publish the
// revision with the member and wait for both acknowledgments, then activate.
func (j *Join) afterGateway(ctx context.Context, gatewayAddress string) error {
	if _, err := netip.ParseAddr(gatewayAddress); err != nil {
		return fmt.Errorf("gateway address %q: %w", gatewayAddress, err)
	}
	j.member().GatewayAddress = gatewayAddress
	var published peergroup.Revision
	return j.steps.run(ctx,
		j.stepAttachHub(),
		a2aStep{
			name: "publish snapshot with member",
			do: func(ctx context.Context) error {
				rev, err := j.p.publishRevision(ctx, j.rec, "join", j.spec.Member, j.spec.Member, map[string][]a2a.AgentSkill{skillsKey(j.spec.Member, j.generation): j.spec.Skills})
				published = rev
				return err
			},
			undo: func(ctx context.Context) error {
				m := j.member()
				if m != nil {
					m.State = peergroup.MemberLeaving
				}
				if err := j.p.commitRevision(ctx, j.rec, "rollback", j.spec.Member, "", nil); err != nil {
					// The hub disconnect that follows still cuts connectivity.
					addObligation(j.rec, peergroup.Obligation{
						Kind: peergroup.ObligationSnapshotCommit, Member: j.spec.Member, Generation: j.generation,
						Revision: published, Since: j.p.timestamp(), LastAttemptAt: j.p.timestamp(), LastError: err.Error(),
					})
					return err
				}
				return nil
			},
		},
		a2aStep{
			name: "await snapshot acknowledgments",
			do: func(ctx context.Context) error {
				return j.p.awaitRevision(ctx, j.rec, published)
			},
		},
		a2aStep{
			name: "activate member",
			do: func(ctx context.Context) error {
				m := j.member()
				m.State = peergroup.MemberActive
				m.JoinedAt = j.p.timestamp()
				if err := j.p.inventory.SetState(j.rec.Group, j.rec.GroupGeneration, j.spec.Member, j.generation, peergroup.InventoryMember); err != nil {
					return err
				}
				return j.p.store.Save(j.rec)
			},
			undo: func(ctx context.Context) error {
				if m := j.member(); m != nil {
					m.State = peergroup.MemberJoining
				}
				return nil
			},
		},
	)
}

func (j *Join) stepAttachHub() a2aStep {
	var created, connected bool
	return a2aStep{
		name: "attach hub",
		do: func(ctx context.Context) error {
			p := j.p
			m := j.member()
			att := backend.HubAttachment{Network: m.Network, IPv4: m.HubAddress, Member: m.Name}
			spec := p.hubSpec(j.rec)
			hub, err := p.engine.inspectHub(ctx, j.rec.Group)
			if err != nil {
				return err
			}
			if !hub.Exists {
				spec.Initial = &att
				ref, err := p.engine.StartHub(ctx, spec)
				j.hub = ref
				if ref.Name == "" {
					j.hub = hubRefFor(j.rec)
				}
				if err != nil {
					_ = p.engine.removeHub(context.WithoutCancel(ctx), j.hub)
					return err
				}
				created = true
				return nil
			}
			j.hub = hub.Ref
			if !hub.Running {
				if err := p.engine.restartHub(ctx, spec, hub); err != nil {
					return err
				}
			}
			if err := p.engine.ConnectHub(ctx, j.hub, att); err != nil {
				return err
			}
			connected = true
			return nil
		},
		undo: func(ctx context.Context) error {
			if created {
				return j.p.engine.removeHub(ctx, j.hub)
			}
			if !connected {
				return nil
			}
			m := j.member()
			if err := j.p.engine.DisconnectHub(ctx, j.hub, m.Network); err != nil {
				addObligation(j.rec, peergroup.Obligation{
					Kind: peergroup.ObligationEngineDisconnect, Member: m.Name, Generation: m.Generation,
					Since: j.p.timestamp(), LastAttemptAt: j.p.timestamp(), LastError: err.Error(),
				})
				return err
			}
			return nil
		},
	}
}

// publishRevision publishes rec's membership and records it pending; the
// hub reload is only a nudge.
func (p *PeerGroups) publishRevision(ctx context.Context, rec *peergroup.GroupRecord, purpose, member, joining string, extraSkills map[string][]a2a.AgentSkill) (peergroup.Revision, error) {
	m, s, err := p.snapshotFor(rec, joining, extraSkills)
	if err != nil {
		return 0, err
	}
	rev, err := p.publisher.Publish(ctx, rec.Group, m, s)
	if err != nil {
		return 0, fmt.Errorf("publish snapshot: %w", err)
	}
	rec.Pending = &peergroup.PendingRevision{Revision: rev, Purpose: purpose, Member: member, StartedAt: p.timestamp()}
	if err := p.store.Save(rec); err != nil {
		return rev, err
	}
	if err := p.engine.ReloadHub(ctx, hubRefFor(rec)); err != nil {
		logx.Debugf("A2A hub %s reload nudge failed: %v", rec.Group, err)
	}
	return rev, nil
}

// awaitRevision waits for both readers to apply rev, then commits it in the
// record and prunes, keeping the previously committed revision (CR-A2-1).
func (p *PeerGroups) awaitRevision(ctx context.Context, rec *peergroup.GroupRecord, rev peergroup.Revision) error {
	if _, err := p.waiter.WaitCommitted(ctx, rec.Group, rec.GroupGeneration, rev, p.cfg.CommitTimeout); err != nil {
		return fmt.Errorf("revision %d: %w", rev, err)
	}
	previous := rec.Committed
	rec.Committed = rev
	rec.Pending = nil
	if err := p.store.Save(rec); err != nil {
		return err
	}
	if err := p.publisher.Prune(rec.Group, rev, previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logx.Debugf("Prune A2A snapshots of %s: %v", rec.Group, err)
	}
	return nil
}
