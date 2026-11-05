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
	"sort"
	"strings"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/peergroup"
)

// reconcileLockWait bounds the wait for a group lock held by a running
// lifecycle; reconcile then reports the group busy instead of blocking.
var reconcileLockWait = 3 * time.Second

// ReconcileResult reports one group's reconciliation (proposal 15).
type ReconcileResult struct {
	Group string
	// Busy: another lifecycle holds the group lock; nothing was done.
	Busy bool
	// Skipped says why the group was left untouched: it runs on another
	// engine, or its record names none and nothing of it runs here.
	Skipped  string
	Degraded bool
	Actions  []string
	Problems []string
}

func (r *ReconcileResult) act(format string, args ...any) {
	r.Actions = append(r.Actions, fmt.Sprintf(format, args...))
}

func (r *ReconcileResult) problem(format string, args ...any) {
	r.Degraded = true
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

// ReconcileAll reconciles every group with a record, an inventory or a hub.
func (p *PeerGroups) ReconcileAll(ctx context.Context) ([]ReconcileResult, error) {
	groups := map[string]bool{}
	names, err := p.store.List()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		groups[name] = true
	}
	hubs, err := p.engine.ListHubs(ctx)
	if err != nil {
		return nil, err
	}
	for _, hub := range hubs {
		groups[hub.Ref.Group] = true
	}
	ordered := make([]string, 0, len(groups))
	for name := range groups {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	var results []ReconcileResult
	var errs []error
	for _, group := range ordered {
		res, err := p.Reconcile(ctx, group)
		results = append(results, res)
		if err != nil {
			errs = append(errs, fmt.Errorf("reconcile peer group %s: %w", group, err))
		}
	}
	return results, errors.Join(errs...)
}

// Reconcile repairs one group under its lock: members whose session is gone
// leave, crashed joins are rolled back, uncommitted revisions are
// re-published, obligations are retried, foreign hub attachments are
// disconnected, an empty group is torn down and old inventories are deleted.
func (p *PeerGroups) Reconcile(ctx context.Context, group string) (ReconcileResult, error) {
	res := ReconcileResult{Group: group}
	lockCtx, cancel := context.WithTimeout(ctx, reconcileLockWait)
	unlock, err := p.lockGroup(lockCtx, group)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			res.Busy = true
			return res, nil
		}
		return res, err
	}
	defer unlock()

	rec, err := p.store.Load(group)
	if errors.Is(err, peergroup.ErrGroupNotFound) {
		return res, p.reconcileWithoutRecord(ctx, group, &res)
	}
	if err != nil {
		return res, err
	}
	if err := p.claimEngine(ctx, rec); err != nil {
		if IsForeignEngine(err) {
			res.Skipped = err.Error()
			return res, nil
		}
		return res, err
	}
	hub, err := p.engine.inspectHub(ctx, group)
	if err != nil {
		return res, err
	}
	if hub.Exists && hub.GroupGeneration != string(rec.GroupGeneration) {
		if err := p.engine.removeHub(ctx, hub.Ref); err != nil {
			return res, err
		}
		res.act("removed hub of group generation %s", hub.GroupGeneration)
	}

	expired := rec.ExpireCredentials(p.now())
	if len(expired) > 0 {
		res.act("expired %d host invocation credentials", len(expired))
	}
	if removed, err := p.reconcileMembers(ctx, rec, &res); err != nil || removed {
		return res, err
	}
	p.reconcileAttachments(ctx, rec, &res)
	// Uncommitted revisions are replaced by the record's membership, never committed late.
	purpose := "credential"
	if rec.Pending != nil || hasObligation(rec, peergroup.ObligationSnapshotCommit) {
		purpose = "rollback"
	}
	if purpose == "rollback" || len(expired) > 0 {
		if err := p.commitRevision(ctx, rec, purpose, "", "", nil); err != nil {
			res.problem("re-publish membership: %v", err)
			addObligation(rec, peergroup.Obligation{Kind: peergroup.ObligationSnapshotCommit, Since: p.timestamp(), LastAttemptAt: p.timestamp(), LastError: err.Error()})
		} else {
			clearObligations(rec, peergroup.ObligationSnapshotCommit)
			res.act("committed revision %d", rec.Committed)
		}
	}
	p.retryObligations(ctx, rec, &res)
	p.releaseQuarantines(ctx, rec, &res)
	p.releaseSuspectedDepartures(ctx, rec, &res)
	if err := p.store.Save(rec); err != nil {
		return res, err
	}
	p.deleteOldInventories(ctx, group, rec.GroupGeneration, &res)
	return res, nil
}

// reconcileMembers settles members whose join crashed, whose leave did not
// finish, or whose session container is gone. It reports whether the group
// was torn down.
func (p *PeerGroups) reconcileMembers(ctx context.Context, rec *peergroup.GroupRecord, res *ReconcileResult) (bool, error) {
	for _, m := range append([]peergroup.GroupMember(nil), rec.Members...) {
		reason := ""
		switch m.State {
		case peergroup.MemberJoining:
			reason = "join did not finish"
		case peergroup.MemberLeaving:
			reason = "leave did not finish"
		default:
			session, err := p.engine.memberSession(ctx, m.SessionContainer)
			if err != nil {
				res.problem("inspect session of %s: %v", m.Name, err)
				continue
			}
			switch {
			case !session.Exists:
				reason = "session container is gone"
			case session.Labels[peergroup.LabelMemberGeneration] != string(m.Generation):
				reason = "session container belongs to another member generation"
			case m.Role == a2a.RoleOrchestrator && session.Ended:
				// A stopped orchestrator is an authoritative departure (proposal 13);
				// a stopped worker stays a member and keeps the hub (proposal 15).
				reason = "session container has exited"
			}
		}
		if reason == "" {
			continue
		}
		var leave LeaveResult
		if err := p.leaveLocked(ctx, rec, m, &leave); err != nil {
			return false, fmt.Errorf("remove member %s (%s): %w", m.Name, reason, err)
		}
		res.act("removed member %s: %s", m.Name, reason)
		res.Problems = append(res.Problems, leave.Problems...)
		res.Degraded = res.Degraded || leave.Degraded
		if leave.GroupRemoved {
			res.act("tore down the group")
			p.deleteOldInventories(ctx, rec.Group, "", res)
			return true, nil
		}
	}
	return false, nil
}

func hasObligation(rec *peergroup.GroupRecord, kind string) bool {
	for _, ob := range rec.Obligations {
		if ob.Kind == kind {
			return true
		}
	}
	return false
}

func clearObligations(rec *peergroup.GroupRecord, kind string) {
	kept := rec.Obligations[:0:0]
	for _, ob := range rec.Obligations {
		if ob.Kind != kind {
			kept = append(kept, ob)
		}
	}
	rec.Obligations = kept
}

// retryObligations retries departure acknowledgments and quarantine releases.
func (p *PeerGroups) retryObligations(ctx context.Context, rec *peergroup.GroupRecord, res *ReconcileResult) {
	for _, ob := range append([]peergroup.Obligation(nil), rec.Obligations...) {
		switch ob.Kind {
		case peergroup.ObligationDepartureAck:
			i := findDeparture(rec, ob.Member, ob.Generation)
			if i < 0 {
				removeObligation(rec, ob.Kind, ob.Member, ob.Generation)
				continue
			}
			dep := rec.Departures[i]
			p.retryDepartureAcks(ctx, rec, &dep, a2a.RoleOrchestrator)
			rec.Departures[i] = dep
			if dep.State == peergroup.DepartureComplete {
				res.act("departure of %s confirmed by every worker", dep.Member)
			} else {
				res.problem("departure of %s still unconfirmed", dep.Member)
			}
		case peergroup.ObligationQuarantineRelease:
			p.retryQuarantineRelease(ctx, rec, ob, res)
		}
	}
}

func (p *PeerGroups) retryQuarantineRelease(ctx context.Context, rec *peergroup.GroupRecord, ob peergroup.Obligation, res *ReconcileResult) {
	i := findMember(rec, ob.Member)
	if i < 0 || rec.Members[i].Generation != ob.Generation {
		removeObligation(rec, ob.Kind, ob.Member, ob.Generation)
		return
	}
	w := rec.Members[i]
	principal := a2a.Principal{Group: rec.Group, GroupGeneration: string(rec.GroupGeneration), Member: w.Name, Role: w.Role, Generation: string(w.Generation)}
	wctx, cancel := context.WithTimeout(ctx, p.cfg.DepartureAckTimeout)
	result, err := p.engine.SignalWorker(wctx, backend.SessionRef{Name: w.SessionContainer}, a2a.CtlRequest{Op: a2a.CtlReleaseQuarantine, Principal: principal})
	cancel()
	alive := result.Response.DescendantsAlive
	if err != nil || alive == nil || *alive {
		reason := "descendants still alive"
		if err != nil {
			reason = err.Error()
		}
		addObligation(rec, peergroup.Obligation{Kind: ob.Kind, Member: ob.Member, Generation: ob.Generation, Since: ob.Since, LastAttemptAt: p.timestamp(), LastError: reason})
		res.problem("worker %s stays quarantined: %s", w.Name, reason)
		return
	}
	removeObligation(rec, ob.Kind, ob.Member, ob.Generation)
	rec.Releases = append(rec.Releases, peergroup.Release{Kind: "quarantine", Worker: w.Name, Member: w.Name, Generation: w.Generation, ReleasedAt: p.timestamp()})
	res.act("released quarantine of %s", w.Name)
}

// reconcileAttachments disconnects the hub from bridges that belong to no
// active member and restarts a stopped hub that still has members.
func (p *PeerGroups) reconcileAttachments(ctx context.Context, rec *peergroup.GroupRecord, res *ReconcileResult) {
	hub, err := p.engine.inspectHub(ctx, rec.Group)
	if err != nil {
		res.problem("inspect hub: %v", err)
		return
	}
	if !hub.Exists {
		if len(rec.Members) > 0 {
			res.problem("hub %s is missing; members cannot reach each other until they rejoin", hubRefFor(rec).Name)
		}
		return
	}
	wanted := map[string]bool{}
	for _, m := range rec.Members {
		if m.State == peergroup.MemberActive && m.Network != "" {
			wanted[m.Network] = true
		}
	}
	failed := false
	for network := range hub.Endpoints {
		if wanted[network] {
			continue
		}
		if err := p.engine.DisconnectHub(ctx, hub.Ref, network); err != nil {
			failed = true
			res.problem("disconnect hub from %s: %v", network, err)
			continue
		}
		res.act("disconnected hub from %s", network)
	}
	if !failed {
		clearObligations(rec, peergroup.ObligationEngineDisconnect)
	}
	if !hub.Running && len(wanted) > 0 {
		hub, err = p.engine.inspectHub(ctx, rec.Group)
		if err == nil {
			err = p.engine.restartHub(ctx, p.hubSpec(rec), hub)
		}
		if err != nil {
			res.problem("restart hub: %v", err)
			return
		}
		res.act("restarted hub")
	}
}

// releaseSuspectedDepartures lifts suspected-departure admission stops for an
// orchestrator that is alive with an unchanged generation (proposal 13).
func (p *PeerGroups) releaseSuspectedDepartures(ctx context.Context, rec *peergroup.GroupRecord, res *ReconcileResult) {
	var orch *peergroup.GroupMember
	for i := range rec.Members {
		if rec.Members[i].Role == a2a.RoleOrchestrator && rec.Members[i].State == peergroup.MemberActive {
			orch = &rec.Members[i]
		}
	}
	if orch == nil {
		return
	}
	session, err := p.engine.memberSession(ctx, orch.SessionContainer)
	if err != nil || !session.Running || session.Labels[peergroup.LabelMemberGeneration] != string(orch.Generation) {
		return
	}
	inventory, err := p.inventory.Load(rec.Group, rec.GroupGeneration)
	if err != nil {
		return
	}
	principal := a2a.Principal{Group: rec.Group, GroupGeneration: string(rec.GroupGeneration), Member: orch.Name, Role: orch.Role, Generation: string(orch.Generation)}
	for _, w := range rec.Members {
		if w.Role != a2a.RoleWorker || w.State != peergroup.MemberActive {
			continue
		}
		if !hasSuspectedStop(inventory, rec, w, *orch) {
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, p.cfg.DepartureAckTimeout)
		_, err := p.engine.SignalWorker(wctx, backend.SessionRef{Name: w.SessionContainer}, a2a.CtlRequest{Op: a2a.CtlReleaseAdmission, Principal: principal})
		cancel()
		if err != nil {
			res.problem("release admission stop for %s on %s: %v", orch.Name, w.Name, err)
			continue
		}
		rec.Releases = append(rec.Releases, peergroup.Release{Kind: "admission-stop", Worker: w.Name, Member: orch.Name, Generation: orch.Generation, ReleasedAt: p.timestamp()})
		res.act("released suspected-departure admission stop for %s on %s", orch.Name, w.Name)
	}
}

// hasSuspectedStop reads the worker's restrictions.json from its config store.
func hasSuspectedStop(inventory *peergroup.InventoryFile, rec *peergroup.GroupRecord, w, orch peergroup.GroupMember) bool {
	restrictions := workerRestrictions(inventory, rec, w)
	if restrictions == nil {
		return false
	}
	for _, stop := range restrictions.AdmissionStops {
		if stop.Principal.Member == orch.Name && stop.Principal.Generation == string(orch.Generation) && stop.Reason == peergroup.DepartureSuspected {
			return true
		}
	}
	return false
}

// workerRestrictions reads a worker's restrictions.json through the inventory;
// nil when unreadable or written for another generation.
func workerRestrictions(inventory *peergroup.InventoryFile, rec *peergroup.GroupRecord, w peergroup.GroupMember) *a2aagent.RestrictionsFileData {
	for _, entry := range inventory.Entries {
		if entry.Member != w.Name || entry.MemberGeneration != w.Generation {
			continue
		}
		data, err := os.ReadFile(filepath.Join(entry.StorePath, a2aagent.RestrictionsFile)) // #nosec G304 -- store path from the host-written inventory.
		if err != nil {
			return nil
		}
		var r a2aagent.RestrictionsFileData
		if json.Unmarshal(data, &r) != nil {
			return nil
		}
		if r.Group != rec.Group || r.GroupGeneration != string(rec.GroupGeneration) || r.Worker != w.Name || r.WorkerGeneration != string(w.Generation) {
			return nil
		}
		return &r
	}
	return nil
}

// releaseQuarantines asks every running worker whose table records a
// quarantine without a release obligation (cancel, deadline, hub loss, epoch
// change) to release it; the worker keeps it while descendants live.
func (p *PeerGroups) releaseQuarantines(ctx context.Context, rec *peergroup.GroupRecord, res *ReconcileResult) {
	inventory, err := p.inventory.Load(rec.Group, rec.GroupGeneration)
	if err != nil {
		return
	}
	for _, w := range append([]peergroup.GroupMember(nil), rec.Members...) {
		if w.Role != a2a.RoleWorker || w.State != peergroup.MemberActive || hasMemberObligation(rec, peergroup.ObligationQuarantineRelease, w) {
			continue
		}
		restrictions := workerRestrictions(inventory, rec, w)
		if restrictions == nil || restrictions.Quarantine == nil {
			continue
		}
		// A stopped worker cannot be asked; ps keeps reporting its quarantine.
		session, err := p.engine.memberSession(ctx, w.SessionContainer)
		if err != nil || !session.Running {
			continue
		}
		p.retryQuarantineRelease(ctx, rec, peergroup.Obligation{Kind: peergroup.ObligationQuarantineRelease, Member: w.Name, Generation: w.Generation, Since: p.timestamp()}, res)
	}
}

func hasMemberObligation(rec *peergroup.GroupRecord, kind string, m peergroup.GroupMember) bool {
	for _, ob := range rec.Obligations {
		if ob.Kind == kind && ob.Member == m.Name && ob.Generation == m.Generation {
			return true
		}
	}
	return false
}

// reconcileWithoutRecord removes a hub whose group has no record and deletes
// stores listed by inventories of earlier group generations.
func (p *PeerGroups) reconcileWithoutRecord(ctx context.Context, group string, res *ReconcileResult) error {
	hub, err := p.engine.inspectHub(ctx, group)
	if err != nil {
		return err
	}
	if hub.Exists {
		if err := p.engine.removeHub(ctx, hub.Ref); err != nil {
			return err
		}
		res.act("removed hub without a group record")
	}
	p.deleteOldInventories(ctx, group, "", res)
	return nil
}

// deleteOldInventories deletes the stores of every inventory generation other
// than keep; those groups are gone, so every entry is eligible.
func (p *PeerGroups) deleteOldInventories(ctx context.Context, group string, keep peergroup.Generation, res *ReconcileResult) {
	generations, err := p.inventory.Generations(group)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			res.problem("list inventories: %v", err)
		}
		return
	}
	for _, gen := range generations {
		if gen == keep {
			continue
		}
		file, err := p.inventory.Load(group, gen)
		if err != nil {
			res.problem("load inventory %s: %v", gen, err)
			continue
		}
		for _, entry := range file.Entries {
			if entry.State != peergroup.InventoryEligible {
				if err := p.inventory.SetState(group, gen, entry.Member, entry.MemberGeneration, peergroup.InventoryEligible); err != nil {
					res.problem("mark %s eligible: %v", entry.Member, err)
				}
			}
		}
		if err := p.inventory.DeleteEligible(ctx, group, gen, deleteA2AStore); err != nil {
			res.problem("delete stores of group generation %s: %v", gen, err)
			continue
		}
		res.act("deleted stores of group generation %s", gen)
	}
}

// gcPeerGroupHubs disconnects hubs from session bridges whose session and
// gateway are gone and removes hubs left without any attachment. It needs no
// group lock: a disconnect only cuts connectivity, and reconcile settles the
// group record later.
func gcPeerGroupHubs(ctx context.Context) {
	// The hub operations read no backend state, so package-level GC can use them.
	b := &Backend{}
	hubs, err := b.ListHubs(ctx)
	if err != nil {
		logx.Debugf("Failed to list A2A hubs: %v", err)
		return
	}
	for _, hub := range hubs {
		remaining := 0
		for _, att := range hub.Attachments {
			orphaned, err := hubAttachmentOrphaned(ctx, att.Network)
			if err != nil {
				logx.Debugf("Failed to check A2A hub %s attachment %s: %v", hub.Ref.Name, att.Network, err)
				remaining++
				continue
			}
			if !orphaned {
				remaining++
				continue
			}
			if err := b.DisconnectHub(ctx, hub.Ref, att.Network); err != nil {
				logx.Debugf("Failed to disconnect A2A hub %s from %s: %v", hub.Ref.Name, att.Network, err)
				remaining++
			}
		}
		if remaining == 0 {
			if err := b.StopHub(ctx, hub.Ref); err != nil {
				logx.Debugf("Failed to remove empty A2A hub %s: %v", hub.Ref.Name, err)
			}
		}
	}
}

// hubAttachmentOrphaned reports whether a hub endpoint's session bridge is
// gone or belongs to a session whose containers no longer exist.
func hubAttachmentOrphaned(ctx context.Context, network string) (bool, error) {
	info, err := networkInspect(ctx, network)
	if err != nil {
		if dockercmd.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	owner, _, ok := sessionNetworkOwner(info)
	if !ok {
		return false, nil
	}
	exists, err := sessionRuntimeExists(ctx, owner)
	return !exists, err
}

// labeledHubEndpoints returns the names among endpoints that are A2A hubs:
// the hub name prefix and the enclave.a2a.hub label.
var labeledHubEndpoints = func(ctx context.Context, names []string) map[string]bool {
	hubs := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, peergroup.HubContainerPrefix) {
			continue
		}
		info, err := hubContainerInspect(ctx, name)
		if err != nil || info.Config == nil {
			continue
		}
		if info.Config.Labels[peergroup.LabelHub] == hubTrueLabel && HubContainerName(info.Config.Labels[peergroup.LabelGroup]) == name {
			hubs[name] = true
		}
	}
	return hubs
}
