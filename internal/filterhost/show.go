// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
)

// Show writes the human description of f. With a preset it adds the merged
// preset, its required scopes, and the grant a session would use. A preset
// that does not resolve fails before anything is written. onGrantProblem
// receives each grant file that does not load. Nil drops them.
func Show(w io.Writer, f *spec.Filter, home string, preset string, onGrantProblem func(problem string)) error {
	var resolved *spec.Preset
	if preset != "" {
		var err error
		if resolved, err = ResolvePreset(f, home, preset); err != nil {
			return err
		}
	}
	title := f.Name
	if f.DisplayName != "" {
		title += " (" + f.DisplayName + ")"
	}
	_, _ = fmt.Fprintln(w, title)
	if f.Description != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", f.Description)
	}
	_, _ = fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(label string, value string) { _, _ = fmt.Fprintf(tw, "%s\t%s\n", label, value) }
	row("hosts:", strings.Join(f.Hosts, ", "))
	if len(f.Confine) > 0 {
		row("confine:", strings.Join(f.Confine, ", "))
	}
	row("credential:", describeCredential(f))
	row("env:", strings.Join(f.Credential.Env, ", "))
	_ = tw.Flush()

	_, _ = fmt.Fprintln(w, "\ncapabilities:")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, name := range filter.SortedKeys(f.Capabilities) {
		c := f.Capabilities[name]
		scopes := strings.Join(shortScopes(f, c.Scopes), " ")
		if c.Public {
			scopes = "public"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", name, scopes, c.Description)
		for _, cname := range filter.SortedKeys(c.Constraints) {
			con := c.Constraints[cname]
			detail := con.Kind
			if con.Resolver != "" {
				detail += ", resolver " + con.Resolver
			}
			if con.Default != nil {
				detail += ", default " + describeConstraintValue(*con.Default)
			}
			_, _ = fmt.Fprintf(tw, "  \t  %s\t%s\n", cname, detail)
		}
	}
	_ = tw.Flush()

	if len(f.Counters) > 0 {
		_, _ = fmt.Fprintln(w, "\ncounters:")
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, name := range filter.SortedKeys(f.Counters) {
			c := f.Counters[name]
			desc := c.Description
			if c.Unit != "" {
				desc += " (" + c.Unit + ")"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", name, desc)
		}
		_ = tw.Flush()
	}

	presets, err := f.Presets(UserPresetsDir(home, f.Name))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "\npresets:")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, p := range presets {
		name := p.Name
		if p.Name == f.DefaultPreset {
			name += " (default)"
		}
		origin := "shipped"
		switch {
		case p.Shadows:
			origin = "user, shadows shipped"
		case !p.Shipped:
			origin = "user"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", name, origin, p.Description)
	}
	if len(presets) == 0 {
		_, _ = fmt.Fprintln(tw, "  (none)")
	}
	_ = tw.Flush()

	if resolved == nil {
		return nil
	}
	return showPreset(w, f, home, resolved, onGrantProblem)
}

func showPreset(w io.Writer, f *spec.Filter, home string, p *spec.Preset, onGrantProblem func(string)) error {
	_, _ = fmt.Fprintf(w, "\npreset %s (%s, %s)\n", p.Name, presetOrigin(p.Shipped), p.Path)
	if p.Description != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", p.Description)
	}
	if len(p.Chain) > 1 {
		links := make([]string, 0, len(p.Chain)-1)
		for _, link := range p.Chain[1:] {
			links = append(links, link.Name+" ("+presetOrigin(link.Shipped)+")")
		}
		_, _ = fmt.Fprintf(w, "  extends: %s\n", strings.Join(links, " -> "))
	}
	_, _ = fmt.Fprintln(w, "\nenabled:")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, capName := range filter.SortedKeys(f.Capabilities) {
		c := f.Capabilities[capName]
		values, enabled := p.Capabilities[capName]
		if !enabled && !c.Public {
			continue
		}
		var parts []string
		for _, cname := range filter.SortedKeys(values) {
			parts = append(parts, cname+": "+describeConstraintValue(values[cname]))
		}
		label := capName
		if c.Public {
			label += " (public)"
		}
		if len(parts) == 0 {
			_, _ = fmt.Fprintf(tw, "  %s\n", label)
			continue
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", label, strings.Join(parts, " | "))
	}
	_ = tw.Flush()

	if len(p.Quotas) > 0 {
		_, _ = fmt.Fprintln(w, "\nquotas:")
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, counter := range filter.SortedKeys(p.Quotas) {
			_, _ = fmt.Fprintf(tw, "  %s\t%d\n", counter, p.Quotas[counter])
		}
		_ = tw.Flush()
	}

	if f.Credential.OAuth2 == nil {
		return nil
	}
	required := f.RequiredScopes(p)
	_, _ = fmt.Fprintln(w, "\nrequired scopes:")
	for _, s := range required {
		_, _ = fmt.Fprintf(w, "  %s\n", s)
	}
	if len(required) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	}
	if strings.TrimSpace(home) == "" {
		return nil
	}
	g, ok, problems, err := SelectGrant(f, NewStore(home, f.Name), required)
	if err != nil {
		return err
	}
	reportGrantProblems(problems, onGrantProblem)
	if ok {
		_, _ = fmt.Fprintf(w, "\ngrant: %s\n", g.ID)
	} else {
		_, _ = fmt.Fprintf(w, "\ngrant: none, run: %s\n", LoginCommand(f.Name, p.Name))
	}
	return nil
}

func presetOrigin(shipped bool) string {
	if shipped {
		return "shipped"
	}
	return "user"
}

func describeCredential(f *spec.Filter) string {
	header := f.Credential.Inject.Header
	if f.Credential.Inject.Format != "" {
		header += ": " + strings.ReplaceAll(f.Credential.Inject.Format, "%s", "<credential>")
	}
	text := f.Credential.Type + ", injected as " + header
	if s := f.Credential.Static; s != nil {
		var sources []string
		if len(s.Env) > 0 {
			sources = append(sources, "env "+strings.Join(s.Env, ", "))
		}
		if s.File != nil {
			sources = append(sources, "file "+s.File.Path)
		}
		text += ", from " + strings.Join(sources, " or ")
	}
	return text
}

// shortScopes drops the filter's scope prefix for display.
func shortScopes(f *spec.Filter, scopes []string) []string {
	prefix := ""
	if f.Credential.OAuth2 != nil {
		prefix = f.Credential.OAuth2.ScopePrefix
	}
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if prefix != "" {
			s = strings.TrimPrefix(s, prefix)
		}
		out = append(out, s)
	}
	return out
}

func describeConstraintValue(v filter.ConstraintValue) string {
	if v.Any {
		return "*"
	}
	var parts []string
	if len(v.Values) > 0 {
		parts = append(parts, strings.Join(v.Values, ", "))
	}
	if len(v.IDs) > 0 {
		parts = append(parts, "ids "+strings.Join(v.IDs, ", "))
	}
	if len(v.Within) > 0 {
		parts = append(parts, "within "+strings.Join(v.Within, ", "))
	}
	return strings.Join(parts, " ")
}
