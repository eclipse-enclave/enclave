// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

const (
	ctlMaxRequestBytes = 64 << 10
	ctlReadTimeout     = 10 * time.Second
)

// serveCtl answers one CtlRequest per connection on the ctl socket.
func (s *Server) serveCtl(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleCtlConn(ctx, conn)
	}
}

func (s *Server) handleCtlConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(ctlReadTimeout))
	var req a2a.CtlRequest
	var resp a2a.CtlResponse
	if err := json.NewDecoder(io.LimitReader(conn, ctlMaxRequestBytes)).Decode(&req); err != nil {
		resp = a2a.CtlResponse{Error: "invalid ctl request"}
	} else {
		resp = s.HandleCtl(ctx, req)
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// HandleCtl applies one host operation; OK means it reached its goal, so a kept quarantine is not OK.
func (s *Server) HandleCtl(ctx context.Context, req a2a.CtlRequest) a2a.CtlResponse {
	resp := a2a.CtlResponse{Op: req.Op}
	fail := func(err error) a2a.CtlResponse {
		resp.OK = false
		resp.Error = err.Error()
		return resp
	}
	switch req.Op {
	case a2a.CtlDepart:
		ref, err := s.ctlPrincipal(req.Principal)
		if err != nil {
			return fail(err)
		}
		s.markDeparted(ref)
		ack, err := s.exec.Depart(ctx, ref, req.Authoritative)
		resp.Departure = &ack
		if err != nil {
			return fail(err)
		}
		resp.OK = true
	case a2a.CtlReleaseAdmission:
		ref, err := s.ctlPrincipal(req.Principal)
		if err != nil {
			return fail(err)
		}
		if err := s.table.ReleaseAdmission(ref); err != nil {
			return fail(err)
		}
		resp.OK = true
	case a2a.CtlReleaseQuarantine:
		alive, err := s.exec.DescendantsAlive()
		resp.DescendantsAlive = &alive
		if err != nil {
			return fail(fmt.Errorf("check descendants: %w", err))
		}
		if !alive {
			if err := s.table.SetQuarantine(nil); err != nil {
				return fail(err)
			}
		}
		quarantined, err := s.quarantined()
		resp.Quarantined = &quarantined
		if err != nil {
			return fail(err)
		}
		if quarantined {
			return fail(errors.New("descendants still alive; quarantine kept"))
		}
		resp.OK = true
	case a2a.CtlStatus:
		quarantined, err := s.quarantined()
		resp.Quarantined = &quarantined
		if err != nil {
			return fail(err)
		}
		alive, err := s.exec.DescendantsAlive()
		resp.DescendantsAlive = &alive
		if err != nil {
			return fail(fmt.Errorf("check descendants: %w", err))
		}
		resp.OK = true
	default:
		return fail(fmt.Errorf("unknown ctl op %q", req.Op))
	}
	return resp
}

func (s *Server) quarantined() (bool, error) {
	restr, err := s.table.Restrictions()
	if err != nil {
		return true, err
	}
	return restr.Quarantine != nil, nil
}

// ctlPrincipal accepts only principals of this group generation; workers never own tasks.
func (s *Server) ctlPrincipal(p a2a.Principal) (a2aagent.PrincipalRef, error) {
	if p.Member == "" || p.Generation == "" {
		return a2aagent.PrincipalRef{}, errors.New("principal member and generation are required")
	}
	if (p.Group != "" && p.Group != s.cfg.Group) || (p.GroupGeneration != "" && p.GroupGeneration != s.cfg.GroupGeneration) {
		return a2aagent.PrincipalRef{}, errors.New("principal belongs to another group or group generation")
	}
	if !generationPattern.MatchString(p.Generation) {
		return a2aagent.PrincipalRef{}, errors.New("principal generation must be 16 lowercase hex characters")
	}
	if p.Role != a2a.RoleOrchestrator && p.Role != a2a.RoleHost {
		return a2aagent.PrincipalRef{}, fmt.Errorf("principal role %q owns no tasks", p.Role)
	}
	return a2aagent.PrincipalRef{Member: p.Member, Role: p.Role, Generation: p.Generation}, nil
}

// CallCtl sends req to the adapter listening on socket and returns its answer.
func CallCtl(ctx context.Context, socket string, req a2a.CtlRequest) (a2a.CtlResponse, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return a2a.CtlResponse{}, fmt.Errorf("adapter not reachable on %s: %w", socket, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return a2a.CtlResponse{}, err
	}
	var resp a2a.CtlResponse
	if err := json.NewDecoder(io.LimitReader(conn, ctlMaxRequestBytes)).Decode(&resp); err != nil {
		return a2a.CtlResponse{}, fmt.Errorf("read ctl response: %w", err)
	}
	return resp, nil
}
