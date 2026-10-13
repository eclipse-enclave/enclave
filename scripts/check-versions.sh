#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# check-versions.sh — report pinned versions, lockstep consistency, and
# available updates. Reads from the network; never modifies files.
#
# Usage: scripts/check-versions.sh [--offline]
#   --offline  only check lockstep groups and uncovered pins (no network)
#
# Requires bash, git, sed, and awk; the online checks also need curl, jq, and go.
# Exits 1 when it finds a problem (disagreeing lockstep group, uncovered pin,
# reachable vulnerability, floor above the distribution ceiling, failed
# lookup). Available updates are reported but are not problems.
#
# The website (website/ and its workflows) is deferred and not checked.
set -euo pipefail

cd "$(dirname "$0")/.."

OFFLINE=0
case "${1:-}" in
    "") ;;
    --offline) OFFLINE=1 ;;
    *)
        echo "usage: $0 [--offline]" >&2
        exit 2
        ;;
esac

# Support set: Debian stable and Ubuntu LTS releases in standard support.
# Floors may not exceed the lowest version these ship.
DEBIAN_SUITES="trixie"
UBUNTU_SUITES="jammy noble resolute"

EXCLUDED_PATHS=(':!website' ':!.github/workflows/website-*.yml' ':!scripts/check-versions.sh')

# File glob and line regex of every known pin location. The uncovered-pin
# check fails on pin-shaped lines that match none of these.
KNOWN_PINS=(
    'Dockerfile|^ARG (BASE_IMAGE|AGENT_NODE_IMAGE|YQ_VERSION)='
    'Dockerfile|^FROM (\$\{[A-Z_]+\}|[a-z-]+) AS '
    'Dockerfile.gateway|^FROM docker\.io/library/(golang|alpine):'
    'go.mod|^(go|toolchain) '
    '.github/workflows/*.yml|uses: [^ ]+@[0-9a-f]{40} # v[0-9.]+$'
    '.github/workflows/*.yml|go-version-file: go\.mod$'
    '.github/workflows/ci.yml|go-version: \$\{\{ steps\.go-floor\.outputs\.version \}\}$'
    '.github/workflows/reusable-rpm-build.yml|container: fedora:[0-9]+$'
    'extensions/features/devtools/install.sh|^(GOLANGCI_LINT|GOSEC|GOVULNCHECK)_VERSION='
    'extensions/features/gitlab-cli/install.sh|^GLAB_VERSION=\$\(curl'
    'extensions/features/node-dev/install.sh|NVM_VERSION=\$\(curl'
    'internal/model/types.go|AlpineImage +='
    'runtime-assets/microvm/alpine/build-bundle.sh|^alpine_image='
)
PIN_PATTERN='@sha256:|uses: [^ .][^ ]*@|[A-Z_]+_VERSION=|^FROM |^[[:space:]]*container: |node-version:|go-version|^toolchain |^go [0-9]'

DEVTOOLS=(
    'GOLANGCI_LINT_VERSION github.com/golangci/golangci-lint/v2 golangci-lint'
    'GOSEC_VERSION github.com/securego/gosec/v2 gosec'
    'GOVULNCHECK_VERSION golang.org/x/vuln govulncheck'
)

problems=0
problem() {
    printf '  PROBLEM  %s\n' "$*"
    problems=$((problems + 1))
}
ok() { printf '  ok       %s\n' "$*"; }
info() { printf '  info     %s\n' "$*"; }
update() { printf '  update   %s\n' "$*"; }
section() { printf '\n== %s\n' "$*"; }

# extract FILE REGEX: set REPLY to the first capture group of the first
# matching line, or report a problem.
extract() {
    local line
    REPLY=""
    while IFS= read -r line || [ -n "$line" ]; do
        if [[ $line =~ $2 ]]; then
            REPLY=${BASH_REMATCH[1]}
            return 0
        fi
    done <"$1"
    problem "$1: no line matches /$2/"
}

# expect_same GROUP LABEL=VALUE...: all values must be equal.
expect_same() {
    local group=$1 first=${2#*=} item
    shift
    for item in "$@"; do
        if [ "${item#*=}" != "$first" ]; then
            problem "$group disagrees: $*"
            return 0
        fi
    done
    ok "$group: $first ($(printf '%s\n' "$@" | cut -d= -f1 | paste -sd, - | sed 's/,/, /g'))"
}

version_key() {
    local major minor patch
    IFS=. read -r major minor patch _ <<<"${1#v}"
    printf '%06d%06d%06d' "$((10#${major:-0}))" "$((10#${minor:-0}))" "$((10#${patch:-0}))"
}

version_le() { [[ ! "$(version_key "$1")" > "$(version_key "$2")" ]]; }

minor_of() {
    local major minor
    IFS=. read -r major minor _ <<<"${1#v}"
    printf '%s.%s' "$major" "$minor"
}

major_of() {
    local v=${1#v}
    printf '%s' "${v%%.*}"
}

# sort_versions: read versions on stdin, print the stable ones oldest first.
sort_versions() {
    local v
    while IFS= read -r v; do
        [[ $v =~ ^v?[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || continue
        printf '%s %s\n' "$(version_key "$v")" "$v"
    done | sort | cut -d' ' -f2
}

reverse_lines() { awk '{ line[NR] = $0 } END { for (i = NR; i > 0; i--) print line[i] }'; }

fetch() { curl -fsSL --max-time 30 --retry 2 "$1"; }

# image_digest REPO TAG: print the multi-arch index digest of a Docker Hub tag.
image_digest() {
    local repo=$1 tag=$2 token
    token=$(fetch "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$repo:pull" | jq -r .token) || return 1
    curl -fsSI --max-time 30 -H "Authorization: Bearer $token" \
        -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
        "https://registry-1.docker.io/v2/$repo/manifests/$tag" | tr -d '\r' | awk 'tolower($1) == "docker-content-digest:" { print $2 }'
}

# --- Lockstep groups ----------------------------------------------------------

section "Lockstep groups"

extract go.mod '^go ([0-9]+\.[0-9]+)'
go_floor=$REPLY
extract go.mod '^go ([0-9.]+)$'
go_directive=$REPLY
extract debian/control 'golang-go \(>= 2:([0-9]+\.[0-9]+)~\)'
deb_go=$REPLY
extract debian/control 'golang-([0-9]+\.[0-9]+)-go'
deb_go_pkg=$REPLY
extract debian/rules '/usr/lib/go-([0-9]+\.[0-9]+)/bin'
deb_rules_go=$REPLY
extract packaging/rpm/enclave.spec '^BuildRequires:[[:space:]]+golang >= ([0-9]+\.[0-9]+)'
rpm_go=$REPLY
extract README.md 'Go ([0-9]+\.[0-9]+) or newer'
readme_go=$REPLY
extract docs/DEV.md 'Go ([0-9]+\.[0-9]+) or newer'
dev_go=$REPLY
extract Makefile 'Install Go ([0-9]+\.[0-9]+)\+'
make_go=$REPLY
# shellcheck disable=SC2016 # literal Makefile text
extract Makefile '"\$\$GO_VER" -lt ([0-9]+)'
make_go_check=$REPLY
expect_same "Go floor" "go.mod=$go_floor" "debian/control=$deb_go" \
    "debian/control alternative=$deb_go_pkg" "debian/rules=$deb_rules_go" \
    "enclave.spec=$rpm_go" "README.md=$readme_go" "docs/DEV.md=$dev_go" \
    "Makefile=$make_go" "Makefile check-go=1.$make_go_check"

extract go.mod '^toolchain go([0-9.]+)'
toolchain=$REPLY
extract Dockerfile.gateway 'golang:([0-9.]+)-alpine'
gateway_go=$REPLY
expect_same "Go toolchain" "go.mod=$toolchain" "Dockerfile.gateway=$gateway_go"

extract Dockerfile.gateway '^FROM docker\.io/library/alpine:([0-9.]+)@'
alpine_runtime=$REPLY
extract Dockerfile.gateway 'golang:[0-9.]+-alpine([0-9.]+)@'
alpine_builder=$REPLY
expect_same "Alpine version" "gateway runtime=$alpine_runtime" "gateway builder=$alpine_builder"

extract Dockerfile.gateway 'library/alpine:[0-9.]+@(sha256:[0-9a-f]{64})'
alpine_gateway=$REPLY
extract internal/model/types.go 'AlpineImage +=.*alpine@(sha256:[0-9a-f]{64})'
alpine_model=$REPLY
extract runtime-assets/microvm/alpine/build-bundle.sh 'alpine@(sha256:[0-9a-f]{64})'
alpine_bundle=$REPLY
expect_same "Alpine digest" "Dockerfile.gateway=$alpine_gateway" \
    "internal/model/types.go=$alpine_model" "build-bundle.sh=$alpine_bundle"

extract debian/control 'git \(>= 1:([0-9.]+)\)'
deb_git=$REPLY
extract packaging/rpm/enclave.spec '^Requires:[[:space:]]+git >= ([0-9.]+)'
rpm_git=$REPLY
expect_same "git floor" "debian/control=$deb_git" "enclave.spec=$rpm_git"

for entry in "${DEVTOOLS[@]}"; do
    read -r var module name <<<"$entry"
    extract extensions/features/devtools/install.sh "$var:-(v[0-9.]+)"
    installed=$REPLY
    extract extensions/features/devtools/README.md "^- \`$name\` \\(\`(v[0-9.]+)\`\\)"
    documented=$REPLY
    expect_same "$name version" "install.sh=$installed" "README.md=$documented"
done

action_pins=$(git grep -hoE 'uses: [^ .][^ ]*@[0-9a-f]{40} # v[0-9.]+' -- .github/workflows "${EXCLUDED_PATHS[@]}" |
    sed 's/^uses: //' | sort -u)
actions=$(printf '%s\n' "$action_pins" | cut -d@ -f1 | sort -u)
for action in $actions; do
    pins=$(printf '%s\n' "$action_pins" | grep -c "^$action@")
    if [ "$pins" -gt 1 ]; then
        problem "GitHub Action $action is pinned $pins ways: $(printf '%s\n' "$action_pins" | grep "^$action@" | sed 's/.*# //' | paste -sd' ' -)"
    fi
done
ok "GitHub Actions: $(printf '%s\n' "$actions" | wc -l | tr -d ' ') actions checked for one SHA each"

# --- Uncovered pins -----------------------------------------------------------

section "Uncovered pins"

uncovered=0
while IFS= read -r hit; do
    file=${hit%%:*}
    content=${hit#*:}
    content=${content#*:}
    covered=0
    for entry in "${KNOWN_PINS[@]}"; do
        # shellcheck disable=SC2053 # the left side of the entry is a glob
        if [[ $file == ${entry%%|*} && $content =~ ${entry#*|} ]]; then
            covered=1
            break
        fi
    done
    if [ "$covered" -eq 0 ]; then
        problem "not in the inventory: $hit"
        uncovered=$((uncovered + 1))
    fi
done < <(git grep -nE "$PIN_PATTERN" -- . ':!*.md' ':!go.sum' ':!*_test.go' "${EXCLUDED_PATHS[@]}")
[ "$uncovered" -gt 0 ] || ok "every pin-shaped line is a known location"

if [ "$OFFLINE" -eq 1 ]; then
    printf '\n%d problem(s)\n' "$problems"
    [ "$problems" -eq 0 ]
    exit
fi

for tool in curl jq go; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "Error: $tool is required for the online checks (use --offline to skip them)" >&2
        exit 2
    }
done

# --- Security -----------------------------------------------------------------

extract extensions/features/devtools/install.sh 'GOVULNCHECK_VERSION:-(v[0-9.]+)'
govulncheck_version=$REPLY
section "Security (govulncheck $govulncheck_version, toolchain go$toolchain)"

# shellcheck disable=SC2016 # jq program, not shell
VULN_REPORT='
  [.[] | select(.finding) | .finding] as $f
  | ([$f[] | select(.trace[0].function) | .osv] | unique) as $reached
  | ($reached[] | . as $id | [$f[] | select(.osv == $id)][0]
      | "reachable \($id) \(.trace[0].module)@\(.trace[0].version) fixed in \(.fixed_version // "n/a")"),
    ([$f[] | select(.osv as $id | $reached | index($id) | not)]
      | group_by(.trace[0].module)[]
      | "unreachable \(.[0].trace[0].module)@\(.[0].trace[0].version): \(map(.osv) | unique | length) advisories, fixed in \(map(.fixed_version // "n/a") | unique | join(" "))")
'
# Pin the toolchain: a newer local Go would otherwise scan its own standard library.
if vulns=$(GOTOOLCHAIN="go$toolchain" go run "golang.org/x/vuln/cmd/govulncheck@$govulncheck_version" -format json ./... 2>/dev/null) &&
    report=$(jq -rs "$VULN_REPORT" <<<"$vulns"); then
    while IFS= read -r line; do
        case "$line" in
            reachable*) problem "${line#reachable }" ;;
            unreachable*) info "not called: ${line#unreachable }" ;;
        esac
    done <<<"$report"
    grep -q '^reachable' <<<"$report" || ok "no reachable vulnerabilities"
else
    problem "govulncheck failed; run it directly to see why"
fi

# --- Go toolchain ---------------------------------------------------------------

section "Go toolchain"

if releases=$(fetch https://proxy.golang.org/golang.org/toolchain/@v/list |
    sed -nE 's/^v0\.0\.1-go([0-9]+\.[0-9]+\.[0-9]+)\.linux-amd64$/\1/p' | sort_versions) && [ -n "$releases" ]; then
    supported_minor=$(printf '%s\n' "$releases" | while read -r v; do minor_of "$v"; echo; done | uniq | tail -n 2 | head -n 1)
    newest_minor=$(minor_of "$(printf '%s\n' "$releases" | tail -n 1)")
    target_toolchain=$(printf '%s\n' "$releases" | awk -v m="$supported_minor." 'index($0, m) == 1' | tail -n 1)
    target_builder="golang:$target_toolchain-alpine$alpine_runtime"
    if [ "$toolchain" != "$target_toolchain" ] && version_le "$toolchain" "$target_toolchain"; then
        if builder_digest=$(image_digest library/golang "${target_builder#golang:}") && [ -n "$builder_digest" ]; then
            target_builder+="@$builder_digest"
        else
            info "no digest for $target_builder yet; rerun once the image is published"
        fi
    fi
    if [ "$toolchain" = "$target_toolchain" ]; then
        ok "go$toolchain is the latest patch of the oldest supported minor ($supported_minor; newest $newest_minor)"
    elif [ "$(minor_of "$toolchain")" = "$supported_minor" ]; then
        update "Go toolchain $toolchain -> $target_toolchain (go.mod toolchain; Dockerfile.gateway $target_builder)"
    elif version_le "$toolchain" "$target_toolchain"; then
        problem "go$toolchain is no longer supported; move to go$target_toolchain (go.mod toolchain; Dockerfile.gateway $target_builder)"
    else
        problem "go$toolchain is newer than go$target_toolchain; devtools built by the go command's toolchain switch use $supported_minor and cannot load a newer standard library"
    fi
else
    problem "Go toolchain lookup failed"
fi

# --- Go modules -----------------------------------------------------------------

escape_module() {
    awk '{ out = ""; for (i = 1; i <= length($0); i++) { c = substr($0, i, 1); out = out (c ~ /[A-Z]/ ? "!" tolower(c) : c) } print out }' <<<"$1"
}

# module_update LABEL MODULE CURRENT MAX_GO: report the newest version whose
# go directive does not exceed MAX_GO.
module_update() {
    local label=$1 module=$2 current=$3 max_go=$4 path versions latest allowed="" held="" v required
    path=$(escape_module "$module")
    if ! versions=$(fetch "https://proxy.golang.org/$path/@v/list" | sort_versions) || [ -z "$versions" ]; then
        problem "$label: version lookup failed for $module"
        return 0
    fi
    latest=$(printf '%s\n' "$versions" | tail -n 1)
    for v in $(printf '%s\n' "$versions" | reverse_lines); do
        if version_le "$v" "$current"; then
            allowed=$current
            break
        fi
        required=$(fetch "https://proxy.golang.org/$path/@v/$v.mod" | sed -nE 's/^go ([0-9.]+).*/\1/p' | head -n 1) || {
            problem "$label: go.mod lookup failed for $module@$v"
            return 0
        }
        if version_le "${required:-0}" "$max_go"; then
            allowed=$v
            break
        fi
        [ -n "$held" ] || held="$latest requires go $required"
    done
    allowed=${allowed:-$current}
    if [ "$allowed" != "$current" ]; then
        update "$label $module $current -> $allowed${held:+ (held back: $held)}"
    elif [ -n "$held" ]; then
        info "$label $module $current held back: $held, limit go $max_go"
    else
        ok "$label $module $current"
    fi
}

section "Go modules (limit: go $go_directive)"
while read -r module version; do
    module_update "module" "$module" "$version" "$go_directive"
done < <(awk '/^require \(/ { block = 1; next } block && /^\)/ { block = 0 } block && !/\/\/ indirect/ { print $1, $2 } /^require [^(]/ { print $2, $3 }' go.mod)

section "Devtools (limit: go $toolchain)"
for entry in "${DEVTOOLS[@]}"; do
    read -r var module name <<<"$entry"
    extract extensions/features/devtools/install.sh "$var:-(v[0-9.]+)"
    pinned=$REPLY
    module_update "$var" "$module" "$pinned" "$toolchain"
done

# --- Container images -----------------------------------------------------------

# check_image LABEL REFERENCE: compare a name:tag@digest pin with the registry.
check_image() {
    local label=$1 ref=$2 name tag digest repo current
    name=${ref%%:*}
    name=${name#docker.io/}
    tag=${ref#*:}
    tag=${tag%%@*}
    digest=${ref#*@}
    repo=$name
    [[ $repo == */* ]] || repo="library/$repo"
    if ! current=$(image_digest "$repo" "$tag") || [ -z "$current" ]; then
        problem "$label: digest lookup failed for $name:$tag"
    elif [ "$current" = "$digest" ]; then
        ok "$label $name:$tag digest current"
    else
        update "$label $name:$tag digest -> $current"
    fi
}

section "Container images"
extract Dockerfile '^ARG BASE_IMAGE=(.+)$'
base_image=$REPLY
extract Dockerfile '^ARG AGENT_NODE_IMAGE=(.+)$'
node_image=$REPLY
extract Dockerfile.gateway '^FROM docker\.io/library/(golang:[^ ]+)'
golang_image=$REPLY
extract Dockerfile.gateway '^FROM docker\.io/library/(alpine:[^ ]+)'
alpine_image=$REPLY
check_image "Debian base" "$base_image"
check_image "Node" "$node_image"
check_image "gateway builder" "$golang_image"
check_image "Alpine" "$alpine_image"

if debian_stable=$(fetch https://endoflife.date/api/v1/products/debian | jq -re '.result.releases[0].codename | ascii_downcase'); then
    case "$base_image" in
        "debian:$debian_stable-"*) ok "Debian base follows Debian stable ($debian_stable)" ;;
        *) update "Debian stable is now $debian_stable; base image is ${base_image%%@*} (major change)" ;;
    esac
else
    problem "Debian release lookup failed"
fi

node_major=${node_image#node:}
node_major=${node_major%%-*}
if node_lts=$(fetch https://nodejs.org/dist/index.json | jq -re '[.[] | select(.lts != false)][0].version'); then
    if [ "$(major_of "$node_lts")" = "$node_major" ]; then
        ok "Node image is on the newest LTS major ($node_major)"
    else
        update "Node LTS is now $(major_of "$node_lts"); image uses $node_major (major change)"
    fi
else
    problem "Node LTS lookup failed"
fi

if alpine_latest=$(fetch https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/latest-releases.yaml |
    sed -nE 's/^[[:space:]]*version: ([0-9.]+)$/\1/p' | head -n 1) && [ -n "$alpine_latest" ]; then
    alpine_latest=$(minor_of "$alpine_latest")
    if [ "$alpine_latest" = "$alpine_runtime" ]; then
        ok "Alpine $alpine_runtime is the latest stable branch"
    else
        new_alpine=$(image_digest library/alpine "$alpine_latest" || true)
        new_builder=$(image_digest library/golang "${target_toolchain:-$toolchain}-alpine$alpine_latest" || true)
        update "Alpine $alpine_runtime -> $alpine_latest (alpine:$alpine_latest@$new_alpine, golang:${target_toolchain:-$toolchain}-alpine$alpine_latest@$new_builder); smoke-test the QEMU bundle"
    fi
else
    problem "Alpine release lookup failed"
fi

extract .github/workflows/reusable-rpm-build.yml 'container: fedora:([0-9]+)$'
fedora=$REPLY
if fedora_latest=$(fetch https://endoflife.date/api/v1/products/fedora | jq -re '.result.releases[0].name'); then
    if [ "$fedora_latest" = "$fedora" ]; then
        ok "Fedora CI container $fedora is the latest release"
    else
        update "Fedora CI container $fedora -> $fedora_latest (.github/workflows/reusable-rpm-build.yml)"
    fi
else
    problem "Fedora release lookup failed"
fi

# --- Release tags on GitHub -----------------------------------------------------

# remote_tags OWNER/REPO: print "tag commit-sha" for every tag.
remote_tags() {
    git ls-remote --tags "https://github.com/$1.git" | awk '{
        ref = $2; sub("refs/tags/", "", ref)
        if (ref ~ /\^\{\}$/) { sub(/\^\{\}$/, "", ref); peeled[ref] = $1 } else { plain[ref] = $1 }
    } END { for (r in plain) print r, (r in peeled ? peeled[r] : plain[r]) }'
}

section "GitHub Actions"
for action in $actions; do
    pin=$(printf '%s\n' "$action_pins" | grep "^$action@" | head -n 1)
    sha=${pin#*@}
    sha=${sha%% *}
    version=${pin##*# }
    repo=$(cut -d/ -f1-2 <<<"$action")
    if ! tags=$(remote_tags "$repo") || [ -z "$tags" ]; then
        problem "$action: tag lookup failed"
        continue
    fi
    tagged=$(awk -v t="$version" '$1 == t { print $2 }' <<<"$tags")
    [ "$tagged" = "$sha" ] || problem "$action@$sha is not the commit of $version (${tagged:-tag missing})"
    latest=$(cut -d' ' -f1 <<<"$tags" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort_versions | tail -n 1)
    latest_sha=$(awk -v t="$latest" '$1 == t { print $2 }' <<<"$tags")
    if [ "$latest" = "$version" ]; then
        [ "$tagged" != "$sha" ] || ok "$action $version"
    elif [ "$(major_of "$latest")" != "$(major_of "$version")" ]; then
        update "$action $version -> $latest ($latest_sha) MAJOR, review the changelog"
    else
        update "$action $version -> $latest ($latest_sha)"
    fi
done

section "Downloaded binaries"
extract Dockerfile '^ARG YQ_VERSION=(v[0-9.]+)'
yq_version=$REPLY
if yq_latest=$(remote_tags mikefarah/yq | cut -d' ' -f1 | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort_versions | tail -n 1) && [ -n "$yq_latest" ]; then
    if [ "$yq_latest" = "$yq_version" ]; then
        ok "yq $yq_version"
    else
        update "yq $yq_version -> $yq_latest (Dockerfile YQ_VERSION)"
    fi
else
    problem "yq tag lookup failed"
fi

# --- Distribution ceilings ------------------------------------------------------

join_suites() {
    local suite out=""
    for suite in "$@"; do out+="${out:+,}$suite,$suite-updates,$suite-security"; done
    printf '%s' "$out"
}

# madison PACKAGE: print "release version" for the support set.
# shellcheck disable=SC2086 # the suite lists are split on purpose
madison() {
    local debian ubuntu
    debian=$(fetch "https://qa.debian.org/madison.php?package=$1&text=on&s=$(join_suites $DEBIAN_SUITES)") || return 1
    ubuntu=$(fetch "https://ubuntu-archive-team.ubuntu.com/madison.cgi?package=$1&text=on&a=amd64&s=$(join_suites $UBUNTU_SUITES)") || return 1
    printf '%s\n%s\n' "$debian" "$ubuntu" | awk -F'|' 'NF >= 3 {
        v = $2; s = $3; gsub(/ /, "", v); gsub(/ /, "", s)
        sub(/\/.*/, "", s); sub(/-(updates|security)$/, "", s); sub(/^[0-9]+:/, "", v)
        if (match(v, /^[0-9]+(\.[0-9]+)*/)) print s, substr(v, RSTART, RLENGTH)
    }'
}

# ceiling LABEL PACKAGE...: print the highest version per release and the
# lowest of those, and set CEILING to it. CEILING stays empty unless every
# release in the support set has a version.
ceiling() {
    local label=$1 data="" package release highest summary="" complete=1
    shift
    for package in "$@"; do
        data+=$(madison "$package") || {
            problem "$label: madison lookup failed for $package"
            CEILING=""
            return 0
        }
        data+=$'\n'
    done
    CEILING=""
    for release in $DEBIAN_SUITES $UBUNTU_SUITES; do
        highest=$(awk -v r="$release" '$1 == r { print $2 }' <<<"$data" | sort_versions | tail -n 1)
        if [ -z "$highest" ]; then
            problem "$label: $release ships none of $*"
            complete=0
            continue
        fi
        summary+=" $release=$highest"
        if [ -z "$CEILING" ] || version_le "$highest" "$CEILING"; then
            CEILING=$highest
        fi
    done
    if [ "$complete" -eq 0 ]; then
        CEILING=""
        info "$label ceiling unknown:$summary"
        return 0
    fi
    info "$label ceiling $CEILING:$summary"
}

section "Distribution ceilings ($DEBIAN_SUITES $UBUNTU_SUITES)"
go_packages="golang-go"
minor=${go_floor#*.}
while version_le "1.$minor" "${newest_minor:-$go_floor}"; do
    go_packages+=" golang-1.$minor-go"
    minor=$((minor + 1))
done
# shellcheck disable=SC2086 # word splitting intended
ceiling "Go" $go_packages
if [ -n "$CEILING" ]; then
    if ! version_le "$go_directive" "$CEILING"; then
        problem "Go floor $go_directive is above the ceiling $CEILING"
    elif [ "$go_directive" != "$CEILING" ]; then
        info "Go floor $go_directive may be raised to $CEILING"
    else
        ok "Go floor $go_directive is at the ceiling"
    fi
fi
ceiling "git" git
if [ -n "$CEILING" ]; then
    if version_le "$deb_git" "$CEILING"; then ok "git floor $deb_git within ceiling"; else problem "git floor $deb_git is above the ceiling $CEILING"; fi
fi
ceiling "Docker" docker.io
ceiling "buildx" docker-buildx
info "Dockerfile features must work with the Docker and buildx ceilings"

printf '\n%d problem(s)\n' "$problems"
[ "$problems" -eq 0 ]
