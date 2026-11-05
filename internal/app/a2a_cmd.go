// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/gateway"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

// JSON kinds of the `enclave a2a` verbs (interfaces.md section 10).
const (
	a2aSchemaVersion = "1"
	a2aKindPeers     = "a2a-peers"
	a2aKindTask      = "a2a-task"
)

// Tri-state values and sources of the a2a status fields (interfaces.md section 9).
const (
	a2aYes                = "yes"
	a2aNo                 = "no"
	a2aUnknown            = "unknown"
	a2aSourceWorkerTable  = "worker-table"
	a2aSourceGroupRecord  = "group-record"
	a2aExitDegraded       = 3
	a2aExitWaitElapsed    = 4
	a2aExitTaskNotSuccess = 5
)

var (
	a2aNow                           = time.Now
	a2aResolveHome                   = config.ResolveHostHome
	a2aStdout              io.Writer = os.Stdout
	a2aStdin               io.Reader = os.Stdin
	a2aNewLifecycle                  = newA2ALifecycle
	a2aNewHostAccess                 = newEngineA2AHostAccess
	a2aDefaultStaleness              = 5 * time.Minute
	a2aNonBlockingLifetime           = 2 * time.Minute
)

// a2aState is the host state of every peer group.
type a2aState struct {
	home      string
	stateRoot string
	store     peergroup.GroupStore
	inventory peergroup.Inventory
}

func newA2AState(home string) a2aState {
	stateRoot := config.HostStateRootDir(home)
	return a2aState{
		home:      home,
		stateRoot: stateRoot,
		store:     peergroup.NewGroupStore(stateRoot, config.HostLocksDir(home)),
		inventory: peergroup.NewInventory(stateRoot),
	}
}

// resolveGroup returns the named group, or the only group with a record.
func (s a2aState) resolveGroup(name string) (string, error) {
	if name = strings.TrimSpace(name); name != "" {
		if err := peergroup.ValidateGroupName(name); err != nil {
			return "", err
		}
		return name, nil
	}
	names, err := s.store.List()
	if err != nil {
		return "", fmt.Errorf("list peer groups: %w", err)
	}
	var withRecord []string
	for _, n := range names {
		if _, err := os.Stat(peergroup.GroupRecordPath(s.stateRoot, n)); err == nil {
			withRecord = append(withRecord, n)
		}
	}
	switch len(withRecord) {
	case 0:
		return "", errors.New("no peer group exists")
	case 1:
		return withRecord[0], nil
	default:
		return "", fmt.Errorf("several peer groups exist (%s); pass --peer-group", strings.Join(withRecord, ", "))
	}
}

// a2aTri is one tri-state status field with its timestamp and source.
type a2aTri struct {
	Value  string
	At     *string
	Source *string
}

func unknownTri() a2aTri { return a2aTri{Value: a2aUnknown} }

func knownTri(value, at, source string) a2aTri {
	tri := a2aTri{Value: value, Source: &source}
	if at != "" {
		tri.At = &at
	}
	return tri
}

type a2aMemberStatus struct {
	Quarantine    a2aTri
	AdmissionStop a2aTri
	Departure     a2aTri
}

// a2aGroupView is one group as the host sees it: the record, the published
// membership and the worker tables, read without the group lock.
type a2aGroupView struct {
	record     *peergroup.GroupRecord
	membership *peergroup.Membership
	// tables holds fresh restrictions per worker; a missing key is unreadable or stale.
	tables map[string]*a2aagent.RestrictionsFileData
}

func a2aMemberKey(member string, generation peergroup.Generation) string {
	return member + "\x00" + string(generation)
}

func (s a2aState) loadView(group string, now time.Time, staleness time.Duration) (*a2aGroupView, error) {
	rec, err := s.store.Load(group)
	if err != nil {
		return nil, err
	}
	view := &a2aGroupView{record: rec, membership: readPublishedMembership(s.stateRoot, rec), tables: map[string]*a2aagent.RestrictionsFileData{}}
	inv, err := s.inventory.Load(group, rec.GroupGeneration)
	if err != nil {
		return view, nil
	}
	for _, m := range rec.Members {
		if m.Role != a2a.RoleWorker {
			continue
		}
		for _, entry := range inv.Entries {
			if entry.Member != m.Name || entry.MemberGeneration != m.Generation {
				continue
			}
			if r, ok := readWorkerRestrictions(entry.StorePath, rec, m, now, staleness); ok {
				view.tables[a2aMemberKey(m.Name, m.Generation)] = r
			}
		}
	}
	return view, nil
}

// readPublishedMembership reads the committed membership without its secrets,
// which the host user may not be able to read.
func readPublishedMembership(stateRoot string, rec *peergroup.GroupRecord) *peergroup.Membership {
	if rec.Committed == 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(peergroup.SnapshotDir(stateRoot, rec.Group, rec.Committed), peergroup.MembershipFile))
	if err != nil {
		return nil
	}
	var m peergroup.Membership
	if json.Unmarshal(data, &m) != nil || m.GroupGeneration != rec.GroupGeneration || m.Revision != rec.Committed {
		return nil
	}
	return &m
}

// readWorkerRestrictions returns the worker's restrictions.json when it
// belongs to this worker generation and its heartbeat is within staleness.
func readWorkerRestrictions(storePath string, rec *peergroup.GroupRecord, m peergroup.GroupMember, now time.Time, staleness time.Duration) (*a2aagent.RestrictionsFileData, bool) {
	data, err := os.ReadFile(filepath.Join(storePath, a2aagent.RestrictionsFile)) // #nosec G304 -- store path from the host-written inventory.
	if err != nil {
		return nil, false
	}
	var r a2aagent.RestrictionsFileData
	if json.Unmarshal(data, &r) != nil || r.SchemaVersion != peergroup.SchemaVersion {
		return nil, false
	}
	if r.Group != rec.Group || r.GroupGeneration != string(rec.GroupGeneration) || r.Worker != m.Name || r.WorkerGeneration != string(m.Generation) {
		return nil, false
	}
	updated, err := peergroup.ParseTimestamp(r.UpdatedAt)
	if err != nil || now.Sub(updated) > staleness {
		return nil, false
	}
	return &r, true
}

// status computes the tri-state fields of section 9 for one member.
func (v *a2aGroupView) status(m peergroup.GroupMember) a2aMemberStatus {
	st := a2aMemberStatus{Quarantine: unknownTri(), AdmissionStop: unknownTri(), Departure: v.departure(m)}
	switch m.Role {
	case a2a.RoleWorker:
		if r, ok := v.tables[a2aMemberKey(m.Name, m.Generation)]; ok {
			st.Quarantine = knownTri(yesNo(r.Quarantine != nil), r.UpdatedAt, a2aSourceWorkerTable)
			st.AdmissionStop = knownTri(yesNo(len(r.AdmissionStops) > 0), r.UpdatedAt, a2aSourceWorkerTable)
		}
	case a2a.RoleOrchestrator:
		// Orchestrators run no adapter, so nothing can quarantine them (Q6).
		st.Quarantine = knownTri(a2aNo, m.JoinedAt, a2aSourceGroupRecord)
		st.AdmissionStop = v.orchestratorAdmissionStop(m)
	}
	return st
}

// orchestratorAdmissionStop is yes when any active worker's table stops this
// principal, unknown when a table is missing and none says yes.
func (v *a2aGroupView) orchestratorAdmissionStop(orch peergroup.GroupMember) a2aTri {
	missing, workers := false, 0
	oldest := ""
	for _, w := range v.record.Members {
		if w.Role != a2a.RoleWorker || w.State != peergroup.MemberActive {
			continue
		}
		workers++
		r, ok := v.tables[a2aMemberKey(w.Name, w.Generation)]
		if !ok {
			missing = true
			continue
		}
		for _, stop := range r.AdmissionStops {
			if stop.Principal.Member == orch.Name && stop.Principal.Generation == string(orch.Generation) {
				return knownTri(a2aYes, r.UpdatedAt, a2aSourceWorkerTable)
			}
		}
		if oldest == "" || r.UpdatedAt < oldest {
			oldest = r.UpdatedAt
		}
	}
	switch {
	case missing:
		return unknownTri()
	case workers == 0:
		return knownTri(a2aNo, orch.JoinedAt, a2aSourceGroupRecord)
	default:
		return knownTri(a2aNo, oldest, a2aSourceWorkerTable)
	}
}

func (v *a2aGroupView) departure(m peergroup.GroupMember) a2aTri {
	for _, d := range v.record.Departures {
		if d.Member == m.Name && d.Generation == m.Generation && d.State != peergroup.DepartureComplete {
			return knownTri(a2aYes, firstNonEmpty(d.UpdatedAt, d.ObservedAt), a2aSourceGroupRecord)
		}
	}
	for _, ob := range v.record.Obligations {
		if ob.Kind == peergroup.ObligationDepartureAck && ob.Member == m.Name && ob.Generation == m.Generation {
			return knownTri(a2aYes, firstNonEmpty(ob.LastAttemptAt, ob.Since), a2aSourceGroupRecord)
		}
	}
	return knownTri(a2aNo, m.JoinedAt, a2aSourceGroupRecord)
}

func yesNo(b bool) string {
	if b {
		return a2aYes
	}
	return a2aNo
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (v *a2aGroupView) skills(m peergroup.GroupMember) []a2a.AgentSkill {
	if v.membership != nil {
		for _, pm := range v.membership.Members {
			if pm.Name == m.Name && pm.Generation == m.Generation {
				return pm.Skills
			}
		}
	}
	return nil
}

// psJSONA2A is the a2a object of one `ps --json` entry (interfaces.md
// section 9). Field names and the tri-state encoding are a contract.
type psJSONA2A struct {
	Group               string        `json:"group"`
	GroupGeneration     string        `json:"groupGeneration"`
	Role                string        `json:"role"`
	Generation          string        `json:"generation"`
	Port                *int          `json:"port"`
	Hub                 *psJSONA2AHub `json:"hub"`
	SnapshotRevision    *uint64       `json:"snapshotRevision"`
	Quarantine          string        `json:"quarantine"`
	QuarantineAt        *string       `json:"quarantineAt"`
	QuarantineSource    *string       `json:"quarantineSource"`
	AdmissionStop       string        `json:"admissionStop"`
	AdmissionStopAt     *string       `json:"admissionStopAt"`
	AdmissionStopSource *string       `json:"admissionStopSource"`
	Departure           string        `json:"departure"`
	DepartureAt         *string       `json:"departureAt"`
	DepartureSource     *string       `json:"departureSource"`
}

type psJSONA2AHub struct {
	Container string `json:"container"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
}

// newPSJSONA2A builds the a2a object of a labelled session. The identity
// comes from the labels; hub, port, revision and status need the group
// record to describe this member generation, else they are null or unknown.
func newPSJSONA2A(view *a2aGroupView, id backend.SessionPeerGroup) *psJSONA2A {
	unknown := unknownTri()
	out := &psJSONA2A{
		Group:           id.Group,
		GroupGeneration: id.GroupGeneration,
		Role:            string(id.Role),
		Generation:      id.MemberGeneration,
		Quarantine:      unknown.Value,
		AdmissionStop:   unknown.Value,
		Departure:       unknown.Value,
	}
	m, ok := view.member(id)
	if !ok {
		return out
	}
	rec := view.record
	st := view.status(m)
	out.Hub = &psJSONA2AHub{Container: rec.HubContainer, Address: m.HubAddress, Port: peergroup.HubPort}
	out.Quarantine, out.QuarantineAt, out.QuarantineSource = st.Quarantine.Value, st.Quarantine.At, st.Quarantine.Source
	out.AdmissionStop, out.AdmissionStopAt, out.AdmissionStopSource = st.AdmissionStop.Value, st.AdmissionStop.At, st.AdmissionStop.Source
	out.Departure, out.DepartureAt, out.DepartureSource = st.Departure.Value, st.Departure.At, st.Departure.Source
	if m.Role == a2a.RoleWorker && m.AdapterPort != 0 {
		port := m.AdapterPort
		out.Port = &port
	}
	if out.Hub.Container == "" {
		out.Hub.Container = backenddocker.HubContainerName(rec.Group)
	}
	if rec.Committed != 0 {
		rev := uint64(rec.Committed)
		out.SnapshotRevision = &rev
	}
	return out
}

// member finds the record entry of the labelled member generation; a nil
// view is an unreadable or missing record.
func (v *a2aGroupView) member(id backend.SessionPeerGroup) (peergroup.GroupMember, bool) {
	if v == nil || string(v.record.GroupGeneration) != id.GroupGeneration {
		return peergroup.GroupMember{}, false
	}
	for _, m := range v.record.Members {
		if m.Name == id.Member && string(m.Generation) == id.MemberGeneration {
			return m, true
		}
	}
	return peergroup.GroupMember{}, false
}

// psA2AObjects maps session container names to their a2a object.
var psA2AObjects = func(opts model.Options, sessions []backend.Session) map[string]*psJSONA2A {
	var state *a2aState
	if home, err := config.ResolveHostHomeReadOnly(); err == nil {
		s := newA2AState(home)
		state = &s
	} else {
		logx.Debugf("resolve host home for peer groups: %v", err)
	}
	return collectPSA2AObjects(state, sessions, a2aNow(), a2aStaleness(opts))
}

// collectPSA2AObjects reads each labelled session's group once; a nil state
// leaves only the label fields.
func collectPSA2AObjects(state *a2aState, sessions []backend.Session, now time.Time, staleness time.Duration) map[string]*psJSONA2A {
	out := map[string]*psJSONA2A{}
	views := map[string]*a2aGroupView{}
	for _, s := range sessions {
		id := s.PeerGroup
		if id == nil {
			continue
		}
		view, seen := views[id.Group]
		if !seen && state != nil {
			var err error
			if view, err = state.loadView(id.Group, now, staleness); err != nil {
				if !errors.Is(err, peergroup.ErrGroupNotFound) {
					logx.Debugf("read peer group %s: %v", id.Group, err)
				}
				view = nil
			}
			views[id.Group] = view
		}
		out[s.Ref.Name] = newPSJSONA2A(view, *id)
	}
	return out
}

func a2aStaleness(opts model.Options) time.Duration {
	if d, err := config.ParseA2ADuration(opts.A2AStatusStaleness); err == nil {
		return d
	}
	return a2aDefaultStaleness
}

// a2aPeersOutput is the `a2a peers --json` object (kind a2a-peers).
type a2aPeersOutput struct {
	SchemaVersion    string          `json:"schemaVersion"`
	Kind             string          `json:"kind"`
	Group            string          `json:"group"`
	GroupGeneration  string          `json:"groupGeneration"`
	SnapshotRevision uint64          `json:"snapshotRevision"`
	Hub              a2aPeersHub     `json:"hub"`
	Members          []a2aPeerMember `json:"members"`
}

type a2aPeersHub struct {
	Container string `json:"container"`
	Port      int    `json:"port"`
}

type a2aPeerMember struct {
	Name                string           `json:"name"`
	Role                string           `json:"role"`
	Generation          string           `json:"generation"`
	State               string           `json:"state"`
	Tool                string           `json:"tool"`
	SessionContainer    string           `json:"sessionContainer"`
	ProjectPath         string           `json:"projectPath"`
	MountMode           string           `json:"mountMode"`
	Description         string           `json:"description"`
	Port                *int             `json:"port"`
	HubAddress          string           `json:"hubAddress"`
	JoinedAt            string           `json:"joinedAt"`
	Skills              []a2a.AgentSkill `json:"skills"`
	Quarantine          string           `json:"quarantine"`
	QuarantineAt        *string          `json:"quarantineAt"`
	QuarantineSource    *string          `json:"quarantineSource"`
	AdmissionStop       string           `json:"admissionStop"`
	AdmissionStopAt     *string          `json:"admissionStopAt"`
	AdmissionStopSource *string          `json:"admissionStopSource"`
	Departure           string           `json:"departure"`
	DepartureAt         *string          `json:"departureAt"`
	DepartureSource     *string          `json:"departureSource"`
}

func buildA2APeers(view *a2aGroupView) a2aPeersOutput {
	rec := view.record
	out := a2aPeersOutput{
		SchemaVersion:    a2aSchemaVersion,
		Kind:             a2aKindPeers,
		Group:            rec.Group,
		GroupGeneration:  string(rec.GroupGeneration),
		SnapshotRevision: uint64(rec.Committed),
		Hub:              a2aPeersHub{Container: firstNonEmpty(rec.HubContainer, backenddocker.HubContainerName(rec.Group)), Port: peergroup.HubPort},
		Members:          []a2aPeerMember{},
	}
	for _, m := range rec.Members {
		st := view.status(m)
		pm := a2aPeerMember{
			Name: m.Name, Role: string(m.Role), Generation: string(m.Generation), State: m.State,
			Tool: m.Tool, SessionContainer: m.SessionContainer, ProjectPath: m.ProjectPath, MountMode: m.MountMode,
			Description: m.Description, HubAddress: m.HubAddress, JoinedAt: m.JoinedAt,
			Skills:     view.skills(m),
			Quarantine: st.Quarantine.Value, QuarantineAt: st.Quarantine.At, QuarantineSource: st.Quarantine.Source,
			AdmissionStop: st.AdmissionStop.Value, AdmissionStopAt: st.AdmissionStop.At, AdmissionStopSource: st.AdmissionStop.Source,
			Departure: st.Departure.Value, DepartureAt: st.Departure.At, DepartureSource: st.Departure.Source,
		}
		if pm.Skills == nil {
			pm.Skills = []a2a.AgentSkill{}
		}
		if m.Role == a2a.RoleWorker && m.AdapterPort != 0 {
			port := m.AdapterPort
			pm.Port = &port
		}
		out.Members = append(out.Members, pm)
	}
	sort.Slice(out.Members, func(i, j int) bool {
		a, b := out.Members[i], out.Members[j]
		if a.Role != b.Role {
			return a.Role == string(a2a.RoleOrchestrator)
		}
		return a.Name < b.Name
	})
	return out
}

func renderA2APeersTable(w io.Writer, out a2aPeersOutput) error {
	if _, err := fmt.Fprintf(w, "Peer group %s (generation %s, revision %d, hub %s)\n", out.Group, out.GroupGeneration, out.SnapshotRevision, out.Hub.Container); err != nil {
		return err
	}
	if len(out.Members) == 0 {
		_, err := fmt.Fprintln(w, "No members.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tROLE\tSTATE\tQUARANTINE\tADMISSION-STOP\tDEPARTURE\tDESCRIPTION"); err != nil {
		return err
	}
	for _, m := range out.Members {
		desc := m.Description
		if desc == "" {
			desc = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Role, m.State, m.Quarantine, m.AdmissionStop, m.Departure, desc); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// runA2A dispatches one `enclave a2a` verb.
func runA2A(opts model.Options, req *cli.A2ARequest) int {
	if req == nil {
		logx.Errorf("missing a2a request")
		return 1
	}
	home, err := a2aResolveHome()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	state := newA2AState(home)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if req.Verb == cli.A2AVerbReconcile {
		if group := a2aReconcileGroup(opts); group != "" {
			if opts, err = a2aEngineFor(opts, state, group, true); err != nil {
				logx.Errorf("%v", err)
				return 1
			}
		}
		return runA2AReconcile(ctx, opts, state)
	}
	group, err := state.resolveGroup(opts.PeerGroup)
	if err != nil {
		return a2aFail(req, "", err)
	}
	switch req.Verb {
	case cli.A2AVerbPeers:
		return runA2APeers(opts, req, state, group)
	case cli.A2AVerbLog:
		return runA2ALog(state, group, req.Tail)
	case cli.A2AVerbSend, cli.A2AVerbTaskGet, cli.A2AVerbTaskCancel, cli.A2AVerbTaskRetry:
		if opts, err = a2aEngineFor(opts, state, group, true); err != nil {
			return a2aFail(req, group, err)
		}
		return runA2AHostVerb(ctx, opts, req, state, group)
	default:
		logx.Errorf("unsupported a2a verb %q", req.Verb)
		return 1
	}
}

// a2aFail reports an error before any task result exists; under --json the
// error still goes out as one object.
func a2aFail(req *cli.A2ARequest, group string, err error) int {
	if req.JSON && req.Verb != cli.A2AVerbPeers {
		_ = writeA2AJSON(a2aStdout, a2aTaskOutput{SchemaVersion: a2aSchemaVersion, Kind: a2aKindTask, Group: group, Error: err.Error()})
	} else if req.JSON {
		_ = writeA2AJSON(a2aStdout, struct {
			SchemaVersion string `json:"schemaVersion"`
			Kind          string `json:"kind"`
			Group         string `json:"group,omitempty"`
			Error         string `json:"error"`
		}{a2aSchemaVersion, a2aKindPeers, group, err.Error()})
	}
	logx.Errorf("%v", err)
	return 1
}

func writeA2AJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runA2APeers(opts model.Options, req *cli.A2ARequest, state a2aState, group string) int {
	view, err := state.loadView(group, a2aNow(), a2aStaleness(opts))
	if errors.Is(err, peergroup.ErrGroupNotFound) {
		err = fmt.Errorf("peer group %s does not exist", group)
	}
	if err != nil {
		return a2aFail(req, group, err)
	}
	out := buildA2APeers(view)
	if req.JSON {
		err = writeA2AJSON(a2aStdout, out)
	} else {
		err = renderA2APeersTable(a2aStdout, out)
	}
	if err != nil {
		logx.Errorf("write peers: %v", err)
		return 1
	}
	return 0
}

// runA2ALog prints audit.jsonl lines verbatim; tail > 0 keeps the last tail.
func runA2ALog(state a2aState, group string, tail int) int {
	path := filepath.Join(peergroup.AuditDir(state.stateRoot, group), peergroup.AuditLogFile)
	f, err := os.Open(path) // #nosec G304 -- fixed audit path under the state root.
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		logx.Errorf("read audit log: %v", err)
		return 1
	}
	defer func() { _ = f.Close() }()
	if err := copyA2ALog(a2aStdout, f, tail); err != nil {
		logx.Errorf("read audit log: %v", err)
		return 1
	}
	return 0
}

func copyA2ALog(w io.Writer, r io.Reader, tail int) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var ring []string
	for scanner.Scan() {
		line := scanner.Text()
		if tail == 0 {
			if _, err := fmt.Fprintln(w, line); err != nil { // #nosec G705 -- audit lines to the terminal, not HTML.
				return err
			}
			continue
		}
		ring = append(ring, line)
		if len(ring) > tail {
			ring = ring[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, line := range ring {
		if _, err := fmt.Fprintln(w, line); err != nil { // #nosec G705 -- audit lines to the terminal, not HTML.
			return err
		}
	}
	return nil
}

// a2aLifecycle is the part of *backenddocker.PeerGroups the verbs drive.
type a2aLifecycle interface {
	Leave(ctx context.Context, spec backenddocker.LeaveSpec) (backenddocker.LeaveResult, error)
	Reconcile(ctx context.Context, group string) (backenddocker.ReconcileResult, error)
	ReconcileAll(ctx context.Context) ([]backenddocker.ReconcileResult, error)
}

// newA2ALifecycle binds the peer group lifecycle to the selected engine. The
// hub image is only referenced: host verbs never build it.
func newA2ALifecycle(ctx context.Context, opts model.Options, home string) (a2aLifecycle, *backenddocker.Backend, error) {
	switch opts.Backend {
	case backend.NameDocker, backend.NamePodman:
	default:
		return nil, nil, fmt.Errorf("peer groups need the docker or podman backend, not %q", opts.Backend)
	}
	host, err := resolveHost()
	if err != nil {
		return nil, nil, err
	}
	host.Home = home
	dockerOpts := dockerBackendOptions(host, model.Paths{}, opts.BuildOptions, opts.RunOptions)
	dockerOpts.Engine = opts.Backend
	be := backenddocker.New(dockerOpts)
	cfg := backenddocker.PeerGroupConfig{HubImage: gateway.A2AHubImage}
	if d, err := config.ParseA2ADuration(opts.A2ACommitTimeout); err == nil {
		cfg.CommitTimeout = d
	}
	if d, err := config.ParseA2ADuration(opts.A2ADepartureAckTimeout); err == nil {
		cfg.DepartureAckTimeout = d
	}
	pg, err := be.PeerGroups(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return pg, be, nil
}

func runA2AReconcile(ctx context.Context, opts model.Options, state a2aState) int {
	if err := checkDocker(); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	lc, _, err := a2aNewLifecycle(ctx, opts, state.home)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	var results []backenddocker.ReconcileResult
	if group := a2aReconcileGroup(opts); group != "" {
		var res backenddocker.ReconcileResult
		res, err = lc.Reconcile(ctx, group)
		results = append(results, res)
	} else {
		results, err = lc.ReconcileAll(ctx)
	}
	return reportA2AReconcile(a2aStdout, results, err, ctx.Err() != nil)
}

// a2aReconcileGroup is the group of `a2a reconcile --peer-group`; a configured
// peer_group names the group sessions join, so without the flag all are covered.
func a2aReconcileGroup(opts model.Options) string {
	if group := strings.TrimSpace(opts.PeerGroup); group != "" && opts.Sources.PeerGroup == model.SourceCLI {
		return group
	}
	return ""
}

// a2aSetEngineBinary points the container CLI wrapper at an engine; tests replace it.
var a2aSetEngineBinary = backenddocker.UseEngine

// a2aEngineFor points opts at the engine the group record names, since the
// group's containers are invisible to any other engine. An explicit --backend
// for another engine, or a caller that cannot switch, gets an error instead.
func a2aEngineFor(opts model.Options, state a2aState, group string, canSwitch bool) (model.Options, error) {
	rec, err := state.store.Load(group)
	if err != nil || rec.Engine == nil || rec.Engine.Name == "" || rec.Engine.Name == opts.Backend {
		return opts, nil
	}
	engine := rec.Engine.Name
	if engine != backend.NameDocker && engine != backend.NamePodman {
		return opts, fmt.Errorf("peer group %s records the unsupported engine %q", group, engine)
	}
	if !canSwitch || opts.Sources.Backend == model.SourceCLI {
		return opts, fmt.Errorf("peer group %s runs on %s, not %s; pass --backend %s", group, engine, opts.Backend, engine)
	}
	logx.Infof("Peer group %s runs on %s; using %s.", group, engine, engine)
	opts.Backend = engine
	a2aSetEngineBinary(engine)
	return opts, nil
}

func reportA2AReconcile(w io.Writer, results []backenddocker.ReconcileResult, err error, interrupted bool) int {
	code := 0
	for _, res := range results {
		switch {
		case res.Busy:
			_, _ = fmt.Fprintf(w, "%s: busy, another enclave command holds the group lock; retry\n", res.Group)
			code = max(code, 1)
		case res.Skipped != "":
			_, _ = fmt.Fprintf(w, "%s: skipped: %s\n", res.Group, res.Skipped)
		case len(res.Actions) == 0 && len(res.Problems) == 0:
			_, _ = fmt.Fprintf(w, "%s: nothing to do\n", res.Group)
		}
		for _, action := range res.Actions {
			_, _ = fmt.Fprintf(w, "%s: %s\n", res.Group, action)
		}
		for _, problem := range res.Problems {
			_, _ = fmt.Fprintf(w, "%s: degraded: %s\n", res.Group, problem)
		}
		if res.Degraded && code == 0 {
			code = a2aExitDegraded
		}
	}
	if interrupted {
		return backend.ExitCodeInterrupted
	}
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return code
}

// PeerGroupStopResult reports `enclave stop --peer-group`.
type PeerGroupStopResult struct {
	Group string
	// Stopped lists the session containers whose stop succeeded.
	Stopped  []string
	Degraded bool
	Problems []string
}

// ExitCode maps the result to section 10: 3 when an obligation stays open.
func (r PeerGroupStopResult) ExitCode() int {
	if r.Degraded {
		return a2aExitDegraded
	}
	return 0
}

// stopPeerGroup stops every member of group and with the last one the hub,
// without prompting. The orchestrator leaves first, while the workers still
// run, so its departure is acknowledged; each member leaves before
// stopSession removes its container, so background and foreground sessions
// are handled alike.
func stopPeerGroup(ctx context.Context, lc a2aLifecycle, store peergroup.GroupStore, group string, stopSession func(ctx context.Context, container string) error) (PeerGroupStopResult, error) {
	res := PeerGroupStopResult{Group: group}
	if err := peergroup.ValidateGroupName(group); err != nil {
		return res, err
	}
	rec, err := store.Load(group)
	if errors.Is(err, peergroup.ErrGroupNotFound) {
		return res, fmt.Errorf("peer group %s does not exist", group)
	}
	if err != nil {
		return res, err
	}
	members := append([]peergroup.GroupMember(nil), rec.Members...)
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].Role == a2a.RoleOrchestrator && members[j].Role != a2a.RoleOrchestrator
	})
	var errs []error
	for _, m := range members {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		left, err := lc.Leave(ctx, backenddocker.LeaveSpec{Group: group, Member: m.Name, Generation: m.Generation})
		if err != nil {
			errs = append(errs, fmt.Errorf("leave %s: %w", m.Name, err))
		}
		if left.Degraded {
			res.Degraded = true
			for _, p := range left.Problems {
				res.Problems = append(res.Problems, m.Name+": "+p)
			}
		}
		if m.SessionContainer == "" || stopSession == nil {
			continue
		}
		if err := stopSession(ctx, m.SessionContainer); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", m.SessionContainer, err))
			continue
		}
		res.Stopped = append(res.Stopped, m.SessionContainer)
	}
	return res, errors.Join(errs...)
}

// runStopPeerGroup is the `enclave stop --peer-group` entry for command_stop.go.
func runStopPeerGroup(opts model.Options, group string, stopSession func(ctx context.Context, container string) error) int {
	home, err := a2aResolveHome()
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	// The sessions are stopped through the backend the caller already built.
	if _, err := a2aEngineFor(opts, newA2AState(home), group, false); err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lc, _, err := a2aNewLifecycle(ctx, opts, home)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	res, err := stopPeerGroup(ctx, lc, newA2AState(home).store, group, stopSession)
	for _, name := range res.Stopped {
		logx.Infof("Stopped %s", name)
	}
	for _, p := range res.Problems {
		logx.Warnf("peer group %s degraded: %s", group, p)
	}
	if ctx.Err() != nil {
		return backend.ExitCodeInterrupted
	}
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	res.Degraded = reconcileStoppedGroup(ctx, lc, group, res.Degraded)
	if res.Degraded {
		logx.Warnf("Run `enclave a2a reconcile --peer-group %s` to retry the open obligations.", group)
	}
	return res.ExitCode()
}

// reconcileStoppedGroup reports whether the group is still degraded after a
// reconcile retried the stop's open obligations (CR-F5a-4); a busy group
// keeps the stop's own verdict.
func reconcileStoppedGroup(ctx context.Context, lc a2aLifecycle, group string, stopDegraded bool) bool {
	res, err := lc.Reconcile(ctx, group)
	if err != nil {
		logx.Warnf("Reconciling peer group %s failed: %v", group, err)
		return true
	}
	return logA2AReconcile([]backenddocker.ReconcileResult{res}, nil) || res.Busy && stopDegraded
}
