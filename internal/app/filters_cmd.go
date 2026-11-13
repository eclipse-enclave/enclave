// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/filterhost"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// filterCommandEnv is what the filter verbs need from the host. Tests replace
// the HTTP client and the browser.
type filterCommandEnv struct {
	paths       model.Paths
	home        string
	out         io.Writer
	httpClient  *http.Client
	openBrowser func(string) error
	now         func() time.Time
}

func newFilterCommandEnv(input *CommandInput) (filterCommandEnv, error) {
	host, err := input.Ctx.Host()
	if err != nil {
		return filterCommandEnv{}, err
	}
	return filterCommandEnv{
		paths:       input.Ctx.Paths,
		home:        host.Home,
		out:         os.Stdout,
		httpClient:  filterhost.NewHTTPClient(),
		openBrowser: filterhost.OpenBrowser,
	}, nil
}

func runFilterCommand(input *CommandInput) int {
	req := input.FilterRequest
	if req == nil {
		logx.Errorf("missing filter request")
		return 1
	}
	env, err := newFilterCommandEnv(input)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	switch input.Action {
	case cli.ActionFiltersShow:
		err = env.show(*req)
	case cli.ActionFiltersLogin:
		err = env.login(context.Background(), *req)
	case cli.ActionFiltersGrants:
		err = env.grants(*req)
	case cli.ActionFiltersLogout:
		err = env.logout(context.Background(), *req)
	default:
		err = fmt.Errorf("unsupported filter action %q", input.Action)
	}
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	return 0
}

func (e filterCommandEnv) load(name string) (*spec.Filter, error) {
	f, err := filterhost.Load(e.paths, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("filter %s is not installed (see `%s filters list`)", name, model.AppName)
	}
	return f, err
}

func (e filterCommandEnv) show(req cli.FilterRequest) error {
	f, err := e.load(req.Name)
	if err != nil {
		return err
	}
	return filterhost.Show(e.out, f, e.home, req.Preset, warnGrantProblem)
}

func (e filterCommandEnv) login(ctx context.Context, req cli.FilterRequest) error {
	f, err := e.load(req.Name)
	if err != nil {
		return err
	}
	if f.Credential.OAuth2 == nil {
		return fmt.Errorf("filter %s uses a static credential: login only applies to oauth2 filters", f.Name)
	}
	if req.Grant != "" {
		if err := filterhost.ValidateGrantID(req.Grant); err != nil {
			return err
		}
	}
	scopes := make([]string, 0, len(req.Scopes))
	for _, s := range req.Scopes {
		scopes = append(scopes, f.FullScope(s))
	}
	store := filterhost.NewStore(e.home, f.Name)

	var grant filterhost.Grant
	if req.Import != "" {
		data, err := filterhost.ReadSecretFile(req.Import)
		if err != nil {
			return err
		}
		grantID := req.Grant
		if grantID == "" && req.Preset != "" {
			grantID = req.Preset
		}
		grant, err = filterhost.Import(ctx, filterhost.ImportRequest{
			Filter: f, Data: data, Scopes: scopes, GrantID: grantID, HTTPClient: e.httpClient, Now: e.now,
		})
		if err != nil {
			return err
		}
	} else {
		client, err := e.loginClient(f, store, req.ClientSecret)
		if err != nil {
			return err
		}
		preset, requested, err := filterhost.LoginScopes(f, e.home, req.Preset, scopes)
		if err != nil {
			return err
		}
		grantID := req.Grant
		if grantID == "" {
			grantID = filterhost.DefaultGrantID(preset, requested)
		}
		var opener func(string) error
		if !req.NoBrowser {
			opener = e.openBrowser
		}
		grant, err = filterhost.Login(ctx, filterhost.LoginRequest{
			Filter: f, Client: client, Scopes: requested, GrantID: grantID,
			Out: e.out, OpenBrowser: opener, HTTPClient: e.httpClient, Now: e.now,
		})
		if err != nil {
			return err
		}
		if missing := f.MissingScopes(grant.Scopes, requested); len(missing) > 0 {
			logx.Warnf("Grant %s lacks scopes that were requested: %s", grant.ID, strings.Join(missing, " "))
		}
	}
	if err := store.SaveGrant(grant); err != nil {
		return err
	}
	logx.Successf("Stored grant %s of filter %s: %s", grant.ID, f.Name, strings.Join(grant.Scopes, " "))
	return nil
}

// warnGrantProblems reports grant files that do not load.
func warnGrantProblems(problems []string) {
	for _, problem := range problems {
		warnGrantProblem(problem)
	}
}

// warnGrantProblem reports a grant file that does not load.
func warnGrantProblem(problem string) {
	logx.Warnf("Skipping a grant that does not load: %s", problem)
}

// loginClient returns the OAuth client for a login: the one in clientSecret,
// which is stored for later logins, or the stored one.
func (e filterCommandEnv) loginClient(f *spec.Filter, store filterhost.Store, clientSecret string) (filterhost.Client, error) {
	if clientSecret != "" {
		data, err := filterhost.ReadSecretFile(clientSecret)
		if err != nil {
			return filterhost.Client{}, err
		}
		client, err := filterhost.ParseClientSecret(data)
		if err != nil {
			return filterhost.Client{}, err
		}
		if err := store.SaveClient(client); err != nil {
			return filterhost.Client{}, err
		}
		return client, nil
	}
	client, ok, err := store.Client()
	if err != nil {
		return filterhost.Client{}, err
	}
	if !ok {
		message := fmt.Sprintf("filter %s has no OAuth client yet: pass --client-secret <file>", f.Name)
		if help := strings.TrimSpace(f.Credential.OAuth2.ClientHelp); help != "" {
			message += ". " + help
		}
		return filterhost.Client{}, errors.New(message)
	}
	return client, nil
}

func (e filterCommandEnv) grants(req cli.FilterRequest) error {
	f, err := e.load(req.Name)
	if err != nil {
		return err
	}
	grants, problems, err := filterhost.NewStore(e.home, f.Name).Grants()
	if err != nil {
		return err
	}
	warnGrantProblems(problems)
	if len(grants) == 0 {
		_, _ = fmt.Fprintf(e.out, "No grants for filter %s (create one with `%s`)\n", f.Name, filterhost.LoginCommand(f.Name, ""))
		return nil
	}
	tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSOURCE\tCREATED\tSCOPES")
	for _, g := range grants {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", g.ID, grantSource(g), g.CreatedAt.UTC().Format(time.RFC3339), strings.Join(g.Scopes, " "))
	}
	return tw.Flush()
}

// grantSource names how a grant was stored: by login or by login --import.
func grantSource(g filterhost.Grant) string {
	if g.Imported() {
		return "import"
	}
	return "login"
}

func (e filterCommandEnv) logout(ctx context.Context, req cli.FilterRequest) error {
	if err := filter.ValidateName(req.Name); err != nil {
		return fmt.Errorf("filter name: %w", err)
	}
	// A removed filter can still have grants. Revoking them needs its spec.
	var f *spec.Filter
	if !req.NoRevoke {
		var err error
		if f, err = e.logoutFilter(req.Name); err != nil {
			return err
		}
	}
	store := filterhost.NewStore(e.home, req.Name)
	grants, problems, err := store.Grants()
	if err != nil {
		return err
	}
	warnGrantProblems(problems)
	var targets []filterhost.Grant
	switch {
	case req.Grant != "":
		g, err := store.Grant(req.Grant)
		if err != nil {
			return err
		}
		targets = []filterhost.Grant{g}
	case req.All:
		targets = grants
	case len(grants) == 1:
		targets = grants
	case len(grants) == 0:
		_, _ = fmt.Fprintf(e.out, "No grants for filter %s\n", req.Name)
		return nil
	default:
		ids := make([]string, 0, len(grants))
		for _, g := range grants {
			ids = append(ids, g.ID)
		}
		return fmt.Errorf("filter %s has %d grants (%s): pass --grant <id> or --all", req.Name, len(grants), strings.Join(ids, ", "))
	}
	// Revoking a grant ends every grant of its OAuth client at providers
	// such as Google. Imported grants live on in the client they came from,
	// so they count as remaining even when deleted here.
	remaining := slices.DeleteFunc(slices.Clone(grants), func(g filterhost.Grant) bool {
		return !g.Imported() && slices.ContainsFunc(targets, func(t filterhost.Grant) bool { return t.ID == g.ID })
	})
	for _, g := range targets {
		sharing := slices.IndexFunc(remaining, func(r filterhost.Grant) bool { return r.ID != g.ID && r.ClientID == g.ClientID })
		switch {
		case req.NoRevoke:
		case f == nil:
			logx.Warnf("Filter %s is not installed, so grant %s is deleted without revoking it", req.Name, g.ID)
		case g.Imported():
			// The client it was exported from still uses its refresh token.
			logx.Infof("Grant %s was imported, so it is deleted without revoking it", g.ID)
		case sharing >= 0:
			logx.Warnf("Grant %s shares its OAuth client with grant %s, so it is deleted without revoking it", g.ID, remaining[sharing].ID)
		default:
			revoked, revokeErr := filterhost.Revoke(ctx, e.httpClient, f, g)
			switch {
			case revokeErr != nil:
				logx.Warnf("Grant %s was not revoked: %v", g.ID, revokeErr)
			case revoked:
				logx.Successf("Revoked grant %s of filter %s", g.ID, req.Name)
			}
		}
		if err := store.DeleteGrant(g.ID); err != nil {
			return err
		}
		logx.Successf("Deleted grant %s of filter %s", g.ID, req.Name)
	}
	return nil
}

// logoutFilter loads the filter whose grants logout revokes, or returns nil
// when its directory is missing, so its grants can only be deleted. Any other
// load failure is an error: deleting would skip the revocation unnoticed.
func (e filterCommandEnv) logoutFilter(name string) (*spec.Filter, error) {
	if _, err := config.FilterExtensionDir(e.paths, name); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	f, err := filterhost.Load(e.paths, name)
	if err != nil {
		return nil, fmt.Errorf("%w (pass --no-revoke to delete the grants without revoking them)", err)
	}
	return f, nil
}
