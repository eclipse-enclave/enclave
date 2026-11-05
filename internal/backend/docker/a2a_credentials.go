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
	"time"

	"enclave/internal/peergroup"
)

const credentialPurpose = "credential"

// IssueHostCredential issues a host invocation credential for group and
// returns once a revision carrying its hash committed (proposal 7). Only the
// hash is stored; the credential goes to the caller alone.
func (p *PeerGroups) IssueHostCredential(ctx context.Context, group string, lifetime time.Duration) (credential string, hash string, err error) {
	unlock, err := p.lockGroup(ctx, group)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	rec, err := p.store.Load(group)
	if err != nil {
		return "", "", err
	}
	// Without this check the reconcile hint below would tear down a group of another engine.
	if err := p.claimEngine(ctx, rec); err != nil {
		return "", "", err
	}
	hub, err := p.engine.inspectHub(ctx, group)
	if err != nil {
		return "", "", err
	}
	if !hub.Running || hub.GroupGeneration != string(rec.GroupGeneration) {
		return "", "", fmt.Errorf("the A2A hub of peer group %s is not running on %s; run `enclave a2a reconcile --peer-group %s --backend %s`", group, p.engineID.Name, group, p.engineID.Name)
	}
	cred, err := p.tokens.NewInvocation(lifetime)
	if err != nil {
		return "", "", err
	}
	rec.ExpireCredentials(p.now())
	rec.AddCredential(cred)
	if err := p.commitCredentials(ctx, rec); err != nil {
		// The published revision may still be applied; the obligation makes reconcile replace it.
		rec.RevokeCredential(cred.Hash)
		return "", "", errors.Join(fmt.Errorf("publish host invocation credential: %w", err), p.store.Save(rec))
	}
	return cred.Token, cred.Hash, nil
}

// RevokeHostCredential unpublishes the credential with hash. A credential
// that is no longer on record (expired, or another group generation) needs
// nothing. With the hub stopped the revision is only published: a restarted
// hub loads it, and reconcile commits it.
func (p *PeerGroups) RevokeHostCredential(ctx context.Context, group, hash string) error {
	unlock, err := p.lockGroup(ctx, group)
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := p.store.Load(group)
	if errors.Is(err, peergroup.ErrGroupNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := p.claimEngine(ctx, rec); err != nil {
		return err
	}
	if !rec.RevokeCredential(hash) {
		return nil
	}
	rec.ExpireCredentials(p.now())
	hub, err := p.engine.inspectHub(ctx, group)
	if err == nil && !hub.Running {
		if _, err := p.publishRevision(ctx, rec, credentialPurpose, "", "", nil); err != nil {
			return errors.Join(fmt.Errorf("publish host credential revocation: %w", err), p.saveWithCommitObligation(rec, err))
		}
		return nil
	}
	if err := p.commitCredentials(ctx, rec); err != nil {
		return errors.Join(fmt.Errorf("host credential revocation did not commit: %w", err), p.store.Save(rec))
	}
	return nil
}

// commitCredentials publishes the record's membership with its current
// credentials and waits for commit. That also settles any earlier revision
// owed by a snapshot-commit obligation; on failure such an obligation is added.
func (p *PeerGroups) commitCredentials(ctx context.Context, rec *peergroup.GroupRecord) error {
	if err := p.commitRevision(ctx, rec, credentialPurpose, "", "", nil); err != nil {
		p.recordCommitObligation(rec, err)
		return err
	}
	if hasObligation(rec, peergroup.ObligationSnapshotCommit) {
		clearObligations(rec, peergroup.ObligationSnapshotCommit)
		return p.store.Save(rec)
	}
	return nil
}

func (p *PeerGroups) recordCommitObligation(rec *peergroup.GroupRecord, cause error) {
	rev := peergroup.Revision(0)
	if rec.Pending != nil {
		rev = rec.Pending.Revision
	}
	now := p.timestamp()
	addObligation(rec, peergroup.Obligation{Kind: peergroup.ObligationSnapshotCommit, Revision: rev, Since: now, LastAttemptAt: now, LastError: cause.Error()})
}

func (p *PeerGroups) saveWithCommitObligation(rec *peergroup.GroupRecord, cause error) error {
	p.recordCommitObligation(rec, cause)
	return p.store.Save(rec)
}
