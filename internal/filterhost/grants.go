// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/filter"
	"enclave/internal/filter/spec"
	"enclave/internal/filter/token"
	"enclave/internal/util"
)

// GrantSchemaVersion versions the grant file.
const GrantSchemaVersion = "1"

const (
	grantExt        = ".json"
	maxSecretFile   = 64 << 10
	privateDirMode  = 0o700
	privateFileMode = 0o600
)

// GrantSourceImport is the Source of a grant stored by `filters login
// --import`. Its refresh token is shared with the client it was exported
// from, so logout never revokes it.
const GrantSourceImport = "import"

// Grant is a stored OAuth2 grant of one filter. Scopes are full scope
// strings. Source is GrantSourceImport for an imported grant and empty for a
// login. String and GoString redact it, so a stray %v cannot log secrets.
type Grant struct {
	SchemaVersion string    `json:"schemaVersion"`
	ID            string    `json:"id"`
	Filter        string    `json:"filter"`
	ClientID      string    `json:"clientId"`
	ClientSecret  string    `json:"clientSecret,omitempty"`
	RefreshToken  string    `json:"refreshToken"`
	Scopes        []string  `json:"scopes"`
	Source        string    `json:"source,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Imported reports whether the grant was stored by `filters login --import`.
func (g Grant) Imported() bool { return g.Source == GrantSourceImport }

// String redacts the grant.
func (g Grant) String() string {
	return fmt.Sprintf("grant %s of filter %s (redacted)", g.ID, g.Filter)
}

// GoString redacts the grant.
func (g Grant) GoString() string { return g.String() }

// Client is a filter's OAuth client configuration.
type Client struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// String redacts the client secret.
func (c Client) String() string { return fmt.Sprintf("oauth client %s (secret redacted)", c.ClientID) }

// GoString redacts the client secret.
func (c Client) GoString() string { return c.String() }

// Store keeps the grants and the client configuration of one filter under
// the host state root. Directories are 0700 and files 0600, written
// atomically.
type Store struct {
	home   string
	filter string
}

// NewStore returns the store of filter for the host home.
func NewStore(home string, filterName string) Store {
	return Store{home: home, filter: filterName}
}

// GrantsDir is the directory holding the grant files.
func (s Store) GrantsDir() string { return config.HostFilterGrantsDir(s.home, s.filter) }

// ClientPath is the client configuration file.
func (s Store) ClientPath() string { return config.HostFilterClientPath(s.home, s.filter) }

// ValidateGrantID checks a grant id. It becomes a file name.
func ValidateGrantID(id string) error {
	if err := filter.ValidateName(id); err != nil {
		return fmt.Errorf("grant id: %w", err)
	}
	return nil
}

func (s Store) grantPath(id string) (string, error) {
	if err := ValidateGrantID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.GrantsDir(), id+grantExt), nil
}

// Grants returns the stored grants sorted by id. A file that does not parse
// is reported in problems and skipped, so one bad file never hides the rest.
func (s Store) Grants() (grants []Grant, problems []string, err error) {
	entries, err := os.ReadDir(s.GrantsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), grantExt)
		if !ok || entry.IsDir() || ValidateGrantID(id) != nil {
			continue
		}
		g, loadErr := s.Grant(id)
		if loadErr != nil {
			problems = append(problems, loadErr.Error())
			continue
		}
		grants = append(grants, g)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	return grants, problems, nil
}

// Grant loads one grant.
func (s Store) Grant(id string) (Grant, error) {
	path, err := s.grantPath(id)
	if err != nil {
		return Grant{}, err
	}
	data, err := ReadSecretFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Grant{}, fmt.Errorf("filter %s has no grant %q: %w", s.filter, id, os.ErrNotExist)
		}
		return Grant{}, err
	}
	var g Grant
	if err := json.Unmarshal(data, &g); err != nil {
		// The decoder error can quote input, which is secret here.
		return Grant{}, fmt.Errorf("grant %s of filter %s: invalid JSON", id, s.filter)
	}
	if err := g.validate(); err != nil {
		return Grant{}, err
	}
	if g.ID != id || g.Filter != s.filter {
		return Grant{}, fmt.Errorf("grant file %s names grant %s of filter %s", path, g.ID, g.Filter)
	}
	return g, nil
}

func (g Grant) validate() error {
	if g.SchemaVersion != GrantSchemaVersion {
		return fmt.Errorf("grant %s: schemaVersion must be %q", g.ID, GrantSchemaVersion)
	}
	if err := ValidateGrantID(g.ID); err != nil {
		return err
	}
	if err := filter.ValidateName(g.Filter); err != nil {
		return fmt.Errorf("grant %s: filter: %w", g.ID, err)
	}
	if g.ClientID == "" || g.RefreshToken == "" {
		return fmt.Errorf("grant %s of filter %s: clientId and refreshToken are required", g.ID, g.Filter)
	}
	if len(g.Scopes) == 0 {
		return fmt.Errorf("grant %s of filter %s: scopes are required", g.ID, g.Filter)
	}
	return nil
}

// SaveGrant writes a grant, replacing one with the same id.
func (s Store) SaveGrant(g Grant) error {
	g.SchemaVersion = GrantSchemaVersion
	g.Filter = s.filter
	g.Scopes = sortedUnique(g.Scopes)
	if err := g.validate(); err != nil {
		return err
	}
	path, err := s.grantPath(g.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(g, "", "  ") // #nosec G117 -- written to a 0600 file in the host state directory.
	if err != nil {
		return err
	}
	return s.writePrivate(path, append(data, '\n'))
}

// DeleteGrant removes a grant file.
func (s Store) DeleteGrant(id string) error {
	path, err := s.grantPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("filter %s has no grant %q: %w", s.filter, id, os.ErrNotExist)
		}
		return err
	}
	return nil
}

// Client loads the client configuration. It reports false when none is
// stored.
func (s Store) Client() (Client, bool, error) {
	data, err := ReadSecretFile(s.ClientPath())
	if errors.Is(err, os.ErrNotExist) {
		return Client{}, false, nil
	}
	if err != nil {
		return Client{}, false, err
	}
	var c Client
	if err := json.Unmarshal(data, &c); err != nil || c.ClientID == "" {
		return Client{}, false, fmt.Errorf("%s: invalid client configuration", s.ClientPath())
	}
	return c, true, nil
}

// SaveClient writes the client configuration.
func (s Store) SaveClient(c Client) error {
	if c.ClientID == "" {
		return fmt.Errorf("client id is required")
	}
	data, err := json.MarshalIndent(c, "", "  ") // #nosec G117 -- written to a 0600 file in the host state directory.
	if err != nil {
		return err
	}
	return s.writePrivate(s.ClientPath(), append(data, '\n'))
}

// DefaultGrantID is the id login and import use without --grant: the preset
// name, or "custom-" and 8 hex digits of the sorted scopes.
func DefaultGrantID(preset string, scopes []string) string {
	if preset != "" {
		return preset
	}
	sum := sha256.Sum256([]byte(strings.Join(sortedUnique(scopes), " ")))
	return "custom-" + hex.EncodeToString(sum[:])[:8]
}

// GrantCandidates projects grants for spec.Filter.SelectGrant.
func GrantCandidates(grants []Grant) []spec.GrantCandidate {
	out := make([]spec.GrantCandidate, 0, len(grants))
	for _, g := range grants {
		out = append(out, spec.GrantCandidate{ID: g.ID, Scopes: g.Scopes, CreatedAt: g.CreatedAt})
	}
	return out
}

// ParseClientSecret reads an OAuth client from Google's client secret file,
// either shape {"installed": {...}} or {"web": {...}}, or from a flat
// {"client_id", "client_secret"} document.
func ParseClientSecret(data []byte) (Client, error) {
	type fields struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	var doc struct {
		Installed *fields `json:"installed"`
		Web       *fields `json:"web"`
		fields
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Client{}, fmt.Errorf("client secret file is not valid JSON")
	}
	f := doc.fields
	switch {
	case doc.Installed != nil:
		f = *doc.Installed
	case doc.Web != nil:
		f = *doc.Web
	}
	if f.ClientID == "" {
		return Client{}, fmt.Errorf("client secret file has no client_id (expected an installed, web, or flat client_id document)")
	}
	if token.HasUnsafe(f.ClientID) || token.HasUnsafe(f.ClientSecret) {
		return Client{}, fmt.Errorf("client secret file has an invalid client_id or client_secret")
	}
	return Client(f), nil
}

// authorizedUser is the authorized_user credential file gcloud and gws export.
type authorizedUser struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func parseAuthorizedUser(data []byte) (authorizedUser, error) {
	var u authorizedUser
	if err := json.Unmarshal(data, &u); err != nil {
		return authorizedUser{}, fmt.Errorf("import file is not valid JSON")
	}
	if u.ClientID == "" || u.RefreshToken == "" {
		return authorizedUser{}, fmt.Errorf("import file needs client_id and refresh_token (an authorized_user document)")
	}
	if token.HasUnsafe(u.ClientID) || token.HasUnsafe(u.ClientSecret) || token.HasUnsafe(u.RefreshToken) {
		return authorizedUser{}, fmt.Errorf("import file has invalid credential values")
	}
	return u, nil
}

// ReadSecretFile reads a credential file, bounded in size.
func ReadSecretFile(path string) ([]byte, error) {
	return util.ReadRegularFile(path, maxSecretFile)
}

// writePrivate writes data atomically with mode 0600 and makes the filters
// state directory, this filter's directory, and the file's directory 0700.
func (s Store) writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return err
	}
	for _, d := range []string{config.HostFiltersStateDir(s.home), config.HostFilterStateDir(s.home, s.filter), dir} {
		if err := os.Chmod(d, privateDirMode); err != nil { // #nosec G302 -- a directory needs its search bit.
			return err
		}
	}
	return util.WriteFileAtomic(path, data, privateFileMode)
}

// sortedUnique returns the non-empty values, sorted and without duplicates.
func sortedUnique(values []string) []string {
	out := slices.DeleteFunc(util.Dedupe(values), func(v string) bool { return v == "" })
	sort.Strings(out)
	return out
}
