// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	"enclave/internal/gateway"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/peergroup"
	"enclave/internal/runtime"
	"enclave/internal/tools"
)

// a2aRunRole is the session's role in its peer group, empty without --peer-group.
func a2aRunRole(opts model.Options) a2a.Role {
	if strings.TrimSpace(opts.PeerGroup) == "" {
		return ""
	}
	if a2a.Role(opts.A2ARole) == a2a.RoleOrchestrator {
		return a2a.RoleOrchestrator
	}
	return a2a.RoleWorker
}

// validateA2ARunOptions holds the run-path rejections that need only the options.
func validateA2ARunOptions(opts model.Options, sources model.OptionSources) error {
	if strings.TrimSpace(opts.PeerGroup) == "" {
		if flag := a2aOptionSetOnCLI(sources); flag != "" {
			return fmt.Errorf("%s requires --peer-group", flag)
		}
		return nil
	}
	if opts.Backend == backend.NameQEMU {
		return fmt.Errorf("--peer-group requires --backend %s or %s; the qemu backend has no network gateway to isolate the group", backend.NameDocker, backend.NamePodman)
	}
	if opts.AllowAllNetwork {
		return fmt.Errorf("--peer-group is incompatible with --allow-all-network; the group route is part of the restricted network gateway")
	}
	if model.SanitizeSessionName(opts.SessionName) == "" {
		return fmt.Errorf("--peer-group requires --name; the session name is the member name in the group")
	}
	if err := config.ValidateA2AOptions(opts.A2AOptions); err != nil {
		return fmt.Errorf("invalid A2A options: %w", err)
	}
	return nil
}

// a2aOptionSetOnCLI names an a2a_* option given on the command line. Every
// session records the a2a_* defaults, and configured values only apply once a
// session joins a group, so only the sources tell a stray flag apart.
func a2aOptionSetOnCLI(sources model.OptionSources) string {
	values := reflect.ValueOf(sources)
	for _, def := range config.OptionDefs() {
		if !strings.HasPrefix(def.Name, "a2a_") {
			continue
		}
		field := values.FieldByName(def.SourceField)
		if !field.IsValid() {
			continue
		}
		if source, ok := field.Interface().(model.OptionSource); !ok || source != model.SourceCLI {
			continue
		}
		if len(def.CLIFlags) > 0 {
			return def.CLIFlags[0].Name
		}
		return def.Name
	}
	return ""
}

// validateA2AProfile holds the run-path rejections that need the tool profile.
func validateA2AProfile(opts model.Options, profile model.Profile, yoloEnabled bool) error {
	switch a2aRunRole(opts) {
	case a2a.RoleWorker:
		if _, err := tools.RequireA2A(profile); err != nil {
			return err
		}
		if profile.YoloFlag == "" {
			return fmt.Errorf("--role worker needs yolo mode, and %s has no yolo flag; nobody can approve permission prompts in serve mode", profile.Name)
		}
		if !yoloEnabled {
			return fmt.Errorf("--role worker needs yolo mode, but it is disabled for this session; nobody can approve permission prompts in serve mode, so drop --no-yolo or yolo=false")
		}
		if opts.Shell {
			return fmt.Errorf("--role worker runs the A2A adapter as the session's main process and cannot start a shell")
		}
	case a2a.RoleOrchestrator:
		if profile.A2A == nil || len(profile.A2A.OrchestratorArgs) == 0 {
			logx.Warnf("%s declares no sandbox.a2a.orchestratorArgs; this orchestrator reaches its peers only through `enclave-a2a-agent client`", profile.Name)
		}
	}
	return nil
}

type a2aEnvValue struct {
	name  string
	value string
	parse func(string) (string, error)
}

func a2aDurationValue(value string) (string, error) {
	d, err := config.ParseA2ADuration(value)
	return d.String(), err
}

func a2aCountValue(value string) (string, error) {
	n, err := config.ParseA2ACount(value)
	return strconv.Itoa(n), err
}

func a2aSizeValue(value string) (string, error) {
	n, err := config.ParseA2ASize(value)
	return strconv.FormatInt(n, 10), err
}

func a2aEnv(values []a2aEnvValue) ([]backend.EnvVar, error) {
	env := make([]backend.EnvVar, 0, len(values))
	for _, v := range values {
		value := v.value
		if v.parse != nil {
			parsed, err := v.parse(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", v.name, err)
			}
			value = parsed
		}
		env = append(env, backend.EnvVar{Name: v.name, Value: value})
	}
	return env, nil
}

// a2aHarnessEnv is ENCLAVE_A2A_HARNESS: the sandbox.a2a block plus the profile's yolo flag (CR-F1-1).
type a2aHarnessEnv struct {
	tools.A2ASpec
	YoloFlag string `json:"yoloFlag,omitempty"`
}

// a2aSessionEnv is the bound part of interfaces.md section 6 for one role;
// identity and tokens come from the join.
func a2aSessionEnv(opts model.Options, role a2a.Role, profile model.Profile) ([]backend.EnvVar, error) {
	values := []a2aEnvValue{
		{a2a.EnvMaxRequestBytes, opts.A2AMaxRequestSize, a2aSizeValue},
		{a2a.EnvReconnectWindow, opts.A2AReconnectWindow, a2aDurationValue},
	}
	if role == a2a.RoleOrchestrator {
		values = append(values, a2aEnvValue{a2a.EnvSendWait, opts.A2ASendWait, a2aDurationValue})
		return a2aEnv(values)
	}
	spec, err := tools.RequireA2A(profile)
	if err != nil {
		return nil, err
	}
	harness, err := json.Marshal(a2aHarnessEnv{A2ASpec: *spec, YoloFlag: profile.YoloFlag})
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", a2a.EnvHarness, err)
	}
	values = append(values,
		a2aEnvValue{a2a.EnvQueueDepth, opts.A2AQueueDepth, a2aCountValue},
		a2aEnvValue{a2a.EnvTaskDeadline, opts.A2ATaskDeadline, a2aDurationValue},
		a2aEnvValue{a2a.EnvMaxResultBytes, opts.A2AMaxResultSize, a2aSizeValue},
		a2aEnvValue{a2a.EnvRecordLimit, opts.A2ARecordLimit, a2aCountValue},
		a2aEnvValue{a2a.EnvRecordMaxAge, opts.A2ARecordMaxAge, a2aDurationValue},
		a2aEnvValue{a2a.EnvContextTTL, opts.A2AContextTTL, a2aDurationValue},
		a2aEnvValue{a2a.EnvTerminationTimeout, opts.A2ATerminationTimeout, a2aDurationValue},
		a2aEnvValue{a2a.EnvHarness, string(harness), nil},
	)
	return a2aEnv(values)
}

// a2aHubEnv is the group-level hub configuration; it applies when this member creates the hub.
func a2aHubEnv(opts model.Options) ([]backend.EnvVar, error) {
	return a2aEnv([]a2aEnvValue{
		{a2a.EnvHubLogMode, opts.A2ALog, nil},
		{a2a.EnvHubRetainedResults, opts.A2AHubRetainedResults, a2aCountValue},
		{a2a.EnvReconnectWindow, opts.A2AReconnectWindow, a2aDurationValue},
		{a2a.EnvMaxRequestBytes, opts.A2AMaxRequestSize, a2aSizeValue},
	})
}

func a2aPeerGroupConfig(opts model.Options, hubImage string) (backenddocker.PeerGroupConfig, error) {
	hubEnv, err := a2aHubEnv(opts)
	if err != nil {
		return backenddocker.PeerGroupConfig{}, err
	}
	cfg := backenddocker.PeerGroupConfig{HubImage: hubImage, HubEnv: hubEnv}
	if d, err := config.ParseA2ADuration(opts.A2ACommitTimeout); err == nil {
		cfg.CommitTimeout = d
	}
	if d, err := config.ParseA2ADuration(opts.A2ADepartureAckTimeout); err == nil {
		cfg.DepartureAckTimeout = d
	}
	return cfg, nil
}

func a2aCardSkills(spec *model.A2ASpec) []a2a.AgentSkill {
	if spec == nil {
		return nil
	}
	skills := make([]a2a.AgentSkill, 0, len(spec.Skills))
	for _, sk := range spec.Skills {
		tags := append([]string{}, sk.Tags...)
		skills = append(skills, a2a.AgentSkill{ID: sk.ID, Name: sk.Name, Description: sk.Description, Tags: tags})
	}
	return skills
}

// a2aJoin is the part of *backenddocker.Join the run path reads.
type a2aJoin interface {
	Session() *backend.PeerGroupSession
	Warnings() []string
	Generation() peergroup.Generation
	LeaveResult() *backenddocker.LeaveResult
}

// a2aJoiner is the part of *backenddocker.PeerGroups the run path drives.
type a2aJoiner interface {
	ReconcileAll(ctx context.Context) ([]backenddocker.ReconcileResult, error)
	Join(ctx context.Context, spec backenddocker.JoinSpec) (a2aJoin, error)
}

type dockerA2AJoiner struct{ pg *backenddocker.PeerGroups }

func (d dockerA2AJoiner) ReconcileAll(ctx context.Context) ([]backenddocker.ReconcileResult, error) {
	return d.pg.ReconcileAll(ctx)
}

func (d dockerA2AJoiner) Join(ctx context.Context, spec backenddocker.JoinSpec) (a2aJoin, error) {
	join, err := d.pg.Join(ctx, spec)
	if err != nil {
		return nil, err
	}
	return join, nil
}

// a2aRun carries a --peer-group session from option checks to its exit code.
type a2aRun struct {
	group      string
	role       a2a.Role
	home       string
	tool       string
	project    model.Project
	desc       string
	skills     []a2a.AgentSkill
	sessionEnv []backend.EnvVar
	peers      a2aJoiner
	join       a2aJoin
}

// prepareA2ARun returns nil for a plain session, which makes no A2A call (F4).
func prepareA2ARun(ctx context.Context, action string, opts model.Options, paths model.Paths, host model.Host, project model.Project, profile model.Profile, be backend.Backend) (*a2aRun, error) {
	if !isRunAction(action) || a2aRunRole(opts) == "" {
		return nil, nil
	}
	return newA2ARun(ctx, opts, paths, host, project, profile, be)
}

// newA2ARun prepares the lifecycle of a --peer-group session: the hub image
// (also the secrets helper image, so it is resolved on every start) and the
// bound env. The join itself runs inside the session start (runtime hook).
func newA2ARun(ctx context.Context, opts model.Options, paths model.Paths, host model.Host, project model.Project, profile model.Profile, be backend.Backend) (*a2aRun, error) {
	dockerBackend, ok := be.(*backenddocker.Backend)
	if !ok {
		return nil, fmt.Errorf("--peer-group requires --backend %s or %s", backend.NameDocker, backend.NamePodman)
	}
	hubImage, err := gateway.EnsureA2AHubImage(ctx, gateway.A2AHubImageConfig{Paths: paths, Home: host.Home, NoRebuild: opts.NoRebuild})
	if err != nil {
		return nil, fmt.Errorf("A2A hub image: %w", err)
	}
	cfg, err := a2aPeerGroupConfig(opts, hubImage)
	if err != nil {
		return nil, err
	}
	role := a2aRunRole(opts)
	sessionEnv, err := a2aSessionEnv(opts, role, profile)
	if err != nil {
		return nil, err
	}
	pg, err := dockerBackend.PeerGroups(ctx, cfg)
	if err != nil {
		return nil, err
	}
	run := &a2aRun{
		group:      strings.TrimSpace(opts.PeerGroup),
		role:       role,
		home:       host.Home,
		tool:       profile.Name,
		project:    project,
		desc:       opts.A2ADescription,
		sessionEnv: sessionEnv,
		peers:      dockerA2AJoiner{pg: pg},
	}
	if role == a2a.RoleWorker {
		run.skills = a2aCardSkills(profile.A2A)
	}
	return run, nil
}

// joinSession is the runtime.PeerGroupJoiner: reconcile, then join. It runs
// after the runtime replaced a stopped container of the same name, so the
// reconcile already lets that container's member leave.
func (r *a2aRun) joinSession(ctx context.Context, m runtime.PeerGroupMember) (*backend.PeerGroupSession, error) {
	results, err := r.peers.ReconcileAll(ctx)
	logA2AReconcile(results, err)
	storeDir, err := hoststore.DirFor(r.home, m.ConfigStore, backend.StoreKindConfig)
	if err != nil {
		return nil, fmt.Errorf("peer group config store: %w", err)
	}
	mountMode := peergroup.MountReadWrite
	if m.ProjectReadOnly {
		mountMode = peergroup.MountReadOnly
	}
	join, err := r.peers.Join(ctx, backenddocker.JoinSpec{
		Group:              r.group,
		Role:               r.role,
		Member:             m.Member,
		SessionContainer:   m.SessionContainer,
		Tool:               r.tool,
		ProjectHash:        r.project.Hash,
		ConfigStoreKey:     filepath.Base(storeDir),
		ConfigStoreDir:     storeDir,
		ContainerConfigDir: m.ContainerConfigDir,
		ProjectPath:        r.project.RealDir,
		MountMode:          mountMode,
		Description:        r.desc,
		Skills:             r.skills,
		SessionEnv:         r.sessionEnv,
	})
	if err != nil {
		return nil, err
	}
	for _, warning := range join.Warnings() {
		logx.Warnf("peer group %s: %s", r.group, warning)
	}
	session := join.Session()
	if r.role == a2a.RoleOrchestrator && m.ContainerConfigDir != "" {
		// CR-A11-1: the orchestrator's MCP client keeps its send journal there.
		session.Env = append(session.Env, backend.EnvVar{
			Name:  a2a.EnvTableDir,
			Value: path.Join(m.ContainerConfigDir, peergroup.ContextTableDirName, string(join.Generation())),
		})
	}
	r.join = join
	return session, nil
}

// exitCode maps a degraded leave of a foreground member to exit 3.
func (r *a2aRun) exitCode(code int) int {
	if r == nil || r.join == nil {
		return code
	}
	res := r.join.LeaveResult()
	if res == nil || !res.Degraded {
		return code
	}
	logx.Warnf("Run `%s a2a reconcile --peer-group %s` to retry the open obligations.", model.AppName, r.group)
	if code == 0 {
		return a2aExitDegraded
	}
	return code
}

// logA2AReconcile reports a lifecycle reconcile and whether an obligation stays open.
func logA2AReconcile(results []backenddocker.ReconcileResult, err error) bool {
	degraded := false
	for _, res := range results {
		if res.Busy {
			logx.Warnf("peer group %s: busy, another enclave command holds the group lock; skipped reconcile", res.Group)
		}
		for _, action := range res.Actions {
			logx.Infof("peer group %s: %s", res.Group, action)
		}
		for _, problem := range res.Problems {
			logx.Warnf("peer group %s degraded: %s", res.Group, problem)
		}
		degraded = degraded || res.Degraded
	}
	if err != nil {
		logx.Warnf("Reconciling peer groups failed: %v", err)
	}
	return degraded
}

// a2aGroupsExist reports whether any peer group state exists, so commands
// without peer groups never touch the A2A lifecycle.
func a2aGroupsExist(store peergroup.GroupStore) bool {
	groups, err := store.List()
	if err != nil {
		logx.Debugf("list peer groups: %v", err)
		return false
	}
	return len(groups) > 0
}

// a2aMemberRef locates a session container in a group record.
type a2aMemberRef struct {
	group  string
	member peergroup.GroupMember
}

// a2aMembersByContainer maps session containers to their group membership.
func a2aMembersByContainer(store peergroup.GroupStore) map[string]a2aMemberRef {
	groups, err := store.List()
	if err != nil {
		logx.Warnf("list peer groups: %v", err)
		return nil
	}
	members := map[string]a2aMemberRef{}
	for _, group := range groups {
		rec, err := store.Load(group)
		if err != nil {
			continue
		}
		for _, m := range rec.Members {
			if m.SessionContainer != "" {
				members[m.SessionContainer] = a2aMemberRef{group: group, member: m}
			}
		}
	}
	return members
}

// orderOrchestratorsFirst lets orchestrators leave while their workers still
// run, so the departure is acknowledged.
func orderOrchestratorsFirst(containers []string, members map[string]a2aMemberRef) []string {
	ordered := append([]string(nil), containers...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return members[ordered[i]].member.Role == a2a.RoleOrchestrator && members[ordered[j]].member.Role != a2a.RoleOrchestrator
	})
	return ordered
}

// a2aLeaver lets members leave their group before their container goes.
type a2aLeaver struct {
	lc      a2aLifecycle
	store   peergroup.GroupStore
	members map[string]a2aMemberRef
	left    map[string]bool
	// degraded holds the groups whose leave left an obligation open.
	degraded map[string]bool
}

// newA2ALeaver returns nil when no peer group exists, so plain cleanups never
// build the lifecycle.
func newA2ALeaver(ctx context.Context, opts model.Options) *a2aLeaver {
	return a2aLeaverFor(ctx, opts, nil)
}

// newA2AStopLeaver returns nil unless one of containers is a group member,
// so a plain stop pays no A2A latency.
func newA2AStopLeaver(ctx context.Context, opts model.Options, containers []string) *a2aLeaver {
	return a2aLeaverFor(ctx, opts, func(members map[string]a2aMemberRef) bool {
		for _, name := range containers {
			if _, ok := members[name]; ok {
				return true
			}
		}
		return false
	})
}

func a2aLeaverFor(ctx context.Context, opts model.Options, needed func(map[string]a2aMemberRef) bool) *a2aLeaver {
	home, err := a2aResolveHome()
	if err != nil {
		return nil
	}
	store := newA2AState(home).store
	if !a2aGroupsExist(store) {
		return nil
	}
	members := a2aMembersByContainer(store)
	if needed != nil && !needed(members) {
		return nil
	}
	lc, _, err := a2aNewLifecycle(ctx, opts, home)
	if err != nil {
		logx.Warnf("Peer group lifecycle unavailable: %v", err)
		return nil
	}
	return &a2aLeaver{lc: lc, store: store, members: members}
}

// leave runs before container is stopped; containers outside any group pass.
func (l *a2aLeaver) leave(ctx context.Context, container string) {
	if l == nil {
		return
	}
	ref, ok := l.members[container]
	if !ok {
		return
	}
	if l.left == nil {
		l.left = map[string]bool{}
	}
	l.left[ref.group] = true
	res, err := l.lc.Leave(ctx, backenddocker.LeaveSpec{Group: ref.group, Member: ref.member.Name, Generation: ref.member.Generation})
	if err != nil {
		logx.Warnf("%s could not leave peer group %s: %v", ref.member.Name, ref.group, err)
		l.markDegraded(ref.group)
		return
	}
	for _, problem := range res.Problems {
		logx.Warnf("peer group %s degraded: %s: %s", ref.group, ref.member.Name, problem)
	}
	if res.Degraded {
		l.markDegraded(ref.group)
	}
	if ref.member.Role == a2a.RoleOrchestrator && !res.GroupRemoved {
		l.printRemainingMembers(ref.group)
	}
}

// printRemainingMembers follows an orchestrator stop: its workers keep running.
func (l *a2aLeaver) printRemainingMembers(group string) {
	rec, err := l.store.Load(group)
	if err != nil || len(rec.Members) == 0 {
		return
	}
	names := make([]string, 0, len(rec.Members))
	for _, m := range rec.Members {
		names = append(names, fmt.Sprintf("%s (%s)", m.Name, m.Role))
	}
	logx.Infof("Peer group %s keeps running: %s. Stop it with `%s stop --peer-group %s`.", group, strings.Join(names, ", "), model.AppName, group)
}

func (l *a2aLeaver) markDegraded(group string) {
	if l.degraded == nil {
		l.degraded = map[string]bool{}
	}
	l.degraded[group] = true
}

// reconcile reports whether an obligation stays open after reconciling every group.
func (l *a2aLeaver) reconcile(ctx context.Context) bool {
	if l == nil {
		return false
	}
	results, err := l.lc.ReconcileAll(ctx)
	degraded := logA2AReconcile(results, err)
	if err != nil {
		return degraded || len(l.degraded) > 0
	}
	// A group absent from the results has nothing left to reconcile.
	for _, res := range results {
		degraded = degraded || res.Busy && l.degraded[res.Group]
	}
	return degraded
}

// reconcileLeftGroups is reconcile limited to the groups a stopped member belonged to.
func (l *a2aLeaver) reconcileLeftGroups(ctx context.Context) bool {
	if l == nil {
		return false
	}
	groups := make([]string, 0, len(l.left))
	for group := range l.left {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	// The reconcile's result replaces the leave's: it retried every open obligation.
	degraded := false
	for _, group := range groups {
		res, err := l.lc.Reconcile(ctx, group)
		if err != nil {
			logx.Warnf("Reconciling peer group %s failed: %v", group, err)
			degraded = true
			continue
		}
		degraded = logA2AReconcile([]backenddocker.ReconcileResult{res}, nil) || res.Busy && l.degraded[group] || degraded
	}
	return degraded
}
