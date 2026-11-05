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
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/backend"
	"enclave/internal/gateway"
	"enclave/internal/logx"
	"enclave/internal/peergroup"
)

// LeaveSpec names the member that leaves (proposal 15 "Leave").
type LeaveSpec struct {
	Group  string
	Member string
	// Generation, when set, must match; a rejoined member is left alone.
	Generation peergroup.Generation
}

// LeaveResult reports a leave. Degraded means an obligation stays open in the
// group record for reconcile (exit code 3 at the CLI).
type LeaveResult struct {
	Left         bool
	Degraded     bool
	Problems     []string
	Departure    *peergroup.Departure
	HubRemoved   bool
	GroupRemoved bool
}

func (r *LeaveResult) degrade(format string, args ...any) {
	r.Degraded = true
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

// Leave removes a member under the group lock. Revocation is never undone:
// a failed step after the leave intent degrades the result and leaves an
// obligation instead of rolling back.
func (p *PeerGroups) Leave(ctx context.Context, spec LeaveSpec) (LeaveResult, error) {
	unlock, err := p.lockGroup(ctx, spec.Group)
	if err != nil {
		return LeaveResult{}, err
	}
	defer unlock()
	rec, err := p.store.Load(spec.Group)
	if errors.Is(err, peergroup.ErrGroupNotFound) {
		return LeaveResult{}, nil
	}
	if err != nil {
		return LeaveResult{}, err
	}
	if err := p.claimEngine(ctx, rec); err != nil {
		return LeaveResult{}, err
	}
	i := findMember(rec, spec.Member)
	if i < 0 || (spec.Generation != "" && rec.Members[i].Generation != spec.Generation) {
		return LeaveResult{}, nil
	}
	var res LeaveResult
	err = p.leaveLocked(ctx, rec, rec.Members[i], &res)
	return res, err
}

// leaveLocked runs the leave steps; the caller holds the group lock.
func (p *PeerGroups) leaveLocked(ctx context.Context, rec *peergroup.GroupRecord, m peergroup.GroupMember, res *LeaveResult) error {
	var steps a2aSteps
	wasState := m.State
	err := steps.run(ctx, a2aStep{
		name: "record leave intent",
		do: func(ctx context.Context) error {
			i := findMember(rec, m.Name)
			rec.Members[i].State = peergroup.MemberLeaving
			if rec.Members[i].LeaveIntentAt == "" {
				rec.Members[i].LeaveIntentAt = p.timestamp()
			}
			return p.store.Save(rec)
		},
		undo: func(ctx context.Context) error {
			if i := findMember(rec, m.Name); i >= 0 {
				rec.Members[i].State = wasState
				rec.Members[i].LeaveIntentAt = m.LeaveIntentAt
			}
			return p.store.Save(rec)
		},
	})
	if err != nil {
		_ = steps.rollback(ctx)
		return err
	}
	// From here on the member is revoked or about to be; nothing is undone.
	committed := true
	hub, hubErr := p.engine.inspectHub(ctx, rec.Group)
	if hubErr == nil && !hub.Running {
		// No hub process holds any snapshot; a restarted hub loads current,
		// so publishing is enough and reconcile commits it after the restart.
		if _, err := p.publishRevision(ctx, rec, "leave", m.Name, "", nil); err != nil {
			res.degrade("publish removal of %s: %v", m.Name, err)
		}
	} else if wasState != peergroup.MemberJoining || rec.Pending != nil || p.publishedIn(rec, m) {
		if err := p.commitRevision(ctx, rec, "leave", m.Name, "", nil); err != nil {
			committed = false
			res.degrade("removal of %s did not commit: %v", m.Name, err)
			rev := peergroup.Revision(0)
			if rec.Pending != nil {
				rev = rec.Pending.Revision
			}
			addObligation(rec, peergroup.Obligation{
				Kind: peergroup.ObligationSnapshotCommit, Member: m.Name, Generation: m.Generation, Revision: rev,
				Since: p.timestamp(), LastAttemptAt: p.timestamp(), LastError: err.Error(),
			})
		}
	}
	if m.Role == a2a.RoleOrchestrator && wasState == peergroup.MemberActive {
		res.Departure = p.collectDepartureAcks(ctx, rec, m, res)
	}
	if m.Network != "" {
		if err := p.engine.DisconnectHub(ctx, hubRefFor(rec), m.Network); err != nil {
			res.degrade("disconnect hub from %s: %v", m.Network, err)
			addObligation(rec, peergroup.Obligation{
				Kind: peergroup.ObligationEngineDisconnect, Member: m.Name, Generation: m.Generation,
				Since: p.timestamp(), LastAttemptAt: p.timestamp(), LastError: err.Error(),
			})
		} else if !committed {
			res.Problems = append(res.Problems, fmt.Sprintf("hub disconnected from %s as the connectivity fallback", m.Network))
		}
	}
	gw, err := p.engine.memberSession(ctx, gateway.ContainerName(m.SessionContainer))
	if err != nil {
		logx.Debugf("Inspect gateway of %s: %v", m.SessionContainer, err)
	}
	if err := p.clearOverlay(ctx, rec.Group, m, gw.Running); err != nil {
		res.Problems = append(res.Problems, err.Error())
	}
	removeMember(rec, m.Name, m.Generation)
	if m.Role == a2a.RoleWorker {
		if err := p.dropOutbound(rec.Group, m.Name, m.Generation); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("drop outbound token of %s: %v", m.Name, err))
		}
	}
	if err := p.inventory.SetState(rec.Group, rec.GroupGeneration, m.Name, m.Generation, peergroup.InventoryEligible); err != nil && !errors.Is(err, fs.ErrNotExist) {
		res.Problems = append(res.Problems, fmt.Sprintf("mark A2A store of %s eligible: %v", m.Name, err))
	}
	if err := p.store.Save(rec); err != nil {
		return err
	}
	res.Left = true
	if len(rec.Members) == 0 {
		return p.teardownLocked(ctx, rec, res)
	}
	return nil
}

// publishedIn reports whether the committed snapshot may still list m.
func (p *PeerGroups) publishedIn(rec *peergroup.GroupRecord, m peergroup.GroupMember) bool {
	data, err := os.ReadFile(filepath.Join(peergroup.CurrentLinkPath(p.stateRoot, rec.Group), peergroup.MembershipFile))
	if err != nil {
		return rec.Committed != 0
	}
	var membership peergroup.Membership
	if json.Unmarshal(data, &membership) != nil {
		return true
	}
	for _, member := range membership.Members {
		if member.Name == m.Name && member.Generation == m.Generation {
			return true
		}
	}
	return false
}

// collectDepartureAcks asks every active worker for the three-fact
// acknowledgment of an authoritative orchestrator departure (proposal 13).
func (p *PeerGroups) collectDepartureAcks(ctx context.Context, rec *peergroup.GroupRecord, departed peergroup.GroupMember, res *LeaveResult) *peergroup.Departure {
	now := p.timestamp()
	dep := peergroup.Departure{
		Member: departed.Name, Generation: departed.Generation,
		Kind: peergroup.DepartureAuthoritative, State: peergroup.DeparturePending,
		ObservedAt: now, UpdatedAt: now,
	}
	if i := findDeparture(rec, departed.Name, departed.Generation); i >= 0 {
		dep = rec.Departures[i]
		dep.Kind = peergroup.DepartureAuthoritative
	}
	p.retryDepartureAcks(ctx, rec, &dep, departed.Role)
	if dep.State != peergroup.DepartureComplete {
		res.degrade("departure of %s is not confirmed by every worker", departed.Name)
	}
	upsertDeparture(rec, dep)
	if err := p.store.Save(rec); err != nil {
		res.Problems = append(res.Problems, err.Error())
	}
	return &dep
}

// retryDepartureAcks signals every active worker that has no good ack yet and
// settles the departure state and its obligations.
func (p *PeerGroups) retryDepartureAcks(ctx context.Context, rec *peergroup.GroupRecord, dep *peergroup.Departure, role a2a.Role) {
	principal := a2a.Principal{
		Group: rec.Group, GroupGeneration: string(rec.GroupGeneration),
		Member: dep.Member, Role: role, Generation: string(dep.Generation),
	}
	var failures []string
	for _, w := range rec.Members {
		if w.Role != a2a.RoleWorker || w.State != peergroup.MemberActive || hasGoodAck(*dep, w) {
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, p.cfg.DepartureAckTimeout)
		result, err := p.engine.SignalWorker(wctx, backend.SessionRef{Name: w.SessionContainer}, a2a.CtlRequest{Op: a2a.CtlDepart, Principal: principal, Authoritative: true})
		cancel()
		ack := result.Response.Departure
		if err != nil || ack == nil {
			if err == nil {
				err = errors.New("no departure acknowledgment in the response")
			}
			failures = append(failures, fmt.Sprintf("%s: %v", w.Name, err))
			continue
		}
		replaceAck(dep, *ack)
		if ack.Termination != a2a.TerminationConfirmed && ack.Termination != a2a.TerminationNotApplicable {
			failures = append(failures, fmt.Sprintf("%s: termination %s", w.Name, ack.Termination))
			// The worker quarantined itself; only reconcile releases it.
			addObligation(rec, peergroup.Obligation{
				Kind: peergroup.ObligationQuarantineRelease, Member: w.Name, Generation: w.Generation,
				Since: p.timestamp(), LastAttemptAt: p.timestamp(), LastError: "termination " + string(ack.Termination),
			})
		}
	}
	dep.UpdatedAt = p.timestamp()
	if len(failures) == 0 {
		dep.State = peergroup.DepartureComplete
		removeObligation(rec, peergroup.ObligationDepartureAck, dep.Member, dep.Generation)
		return
	}
	dep.State = peergroup.DepartureDegraded
	addObligation(rec, peergroup.Obligation{
		Kind: peergroup.ObligationDepartureAck, Member: dep.Member, Generation: dep.Generation,
		Since: p.timestamp(), LastAttemptAt: p.timestamp(), LastError: strings.Join(failures, "; "),
	})
}

func hasGoodAck(dep peergroup.Departure, w peergroup.GroupMember) bool {
	for _, ack := range dep.Acks {
		if ack.Worker == w.Name && ack.WorkerGeneration == string(w.Generation) {
			return ack.Termination == a2a.TerminationConfirmed || ack.Termination == a2a.TerminationNotApplicable
		}
	}
	return false
}

func replaceAck(dep *peergroup.Departure, ack a2a.DepartureAck) {
	for i, existing := range dep.Acks {
		if existing.Worker == ack.Worker && existing.WorkerGeneration == ack.WorkerGeneration {
			dep.Acks[i] = ack
			return
		}
	}
	dep.Acks = append(dep.Acks, ack)
}

func findDeparture(rec *peergroup.GroupRecord, member string, generation peergroup.Generation) int {
	for i, dep := range rec.Departures {
		if dep.Member == member && dep.Generation == generation {
			return i
		}
	}
	return -1
}

func upsertDeparture(rec *peergroup.GroupRecord, dep peergroup.Departure) {
	if i := findDeparture(rec, dep.Member, dep.Generation); i >= 0 {
		rec.Departures[i] = dep
		return
	}
	rec.Departures = append(rec.Departures, dep)
}

// teardownLocked removes the hub and the group once no member is left
// (proposal 15 "Hub lifetime"); stores of departed members are deleted from
// the inventory, and an entry whose deletion fails keeps the inventory file.
func (p *PeerGroups) teardownLocked(ctx context.Context, rec *peergroup.GroupRecord, res *LeaveResult) error {
	hub, err := p.engine.inspectHub(ctx, rec.Group)
	if err != nil {
		return err
	}
	if hub.Exists {
		for network := range hub.Endpoints {
			if err := p.engine.DisconnectHub(ctx, hub.Ref, network); err != nil {
				return fmt.Errorf("disconnect A2A hub from %s before removal: %w", network, err)
			}
		}
		if err := p.engine.StopHub(ctx, hub.Ref); err != nil {
			return err
		}
		res.HubRemoved = true
	}
	if err := p.inventory.DeleteEligible(ctx, rec.Group, rec.GroupGeneration, deleteA2AStore); err != nil {
		res.Problems = append(res.Problems, err.Error())
	}
	if err := p.store.Delete(rec.Group, rec.GroupGeneration); err != nil && !errors.Is(err, peergroup.ErrGroupNotFound) {
		return err
	}
	res.GroupRemoved = true
	return nil
}

// deleteA2AStore deletes a member generation's context table and the
// transcripts its contexts.json lists (config-store relative, CR-P2-3).
func deleteA2AStore(entry peergroup.InventoryEntry) error {
	tableDir := filepath.Clean(entry.StorePath)
	if filepath.Base(filepath.Dir(tableDir)) != peergroup.ContextTableDirName {
		return fmt.Errorf("store path %s is not a context table directory", tableDir)
	}
	storeRoot := filepath.Dir(filepath.Dir(tableDir))
	data, err := os.ReadFile(filepath.Join(tableDir, a2aagent.ContextsFile)) // #nosec G304 -- inventory store path, checked to be a context table directory.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		var contexts a2aagent.ContextsFileData
		if err := json.Unmarshal(data, &contexts); err != nil {
			return fmt.Errorf("read %s: %w", a2aagent.ContextsFile, err)
		}
		for _, record := range contexts.Contexts {
			for _, rel := range record.Transcripts {
				target := filepath.Join(storeRoot, filepath.FromSlash(rel))
				if within, relErr := filepath.Rel(storeRoot, target); relErr != nil || within == "." || strings.HasPrefix(within, "..") || filepath.IsAbs(rel) {
					return fmt.Errorf("transcript path %q escapes the config store", rel)
				}
				if err := os.RemoveAll(target); err != nil {
					return err
				}
			}
		}
	}
	return os.RemoveAll(tableDir)
}
