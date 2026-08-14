# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

%{!?package_version:%global package_version 0.1.0}
%{!?build_version:%global build_version %{package_version}}
%{!?build_commit:%global build_commit unknown}
%{!?build_date:%global build_date unknown}
%global debug_package %{nil}
%global _build_id_links none

# The asset tree is a container build context, not host executables: these
# scripts run inside Alpine and Debian images, never on the packaging host.
# Fedora's brp-mangle-shebangs rewrites /bin/sh to /usr/bin/sh, which does not
# exist in Alpine (no /usr merge), so a mangled gateway-entrypoint.sh fails to
# exec and the DNS gateway container dies on start.
%global __brp_mangle_shebangs_exclude_from ^%{_datadir}/enclave/

Name:           enclave
Version:        %{package_version}
Release:        1%{?dist}
Summary:        Container-based development environment manager
License:        MIT
URL:            https://github.com/eclipse-enclave/enclave
Source0:        %{name}-%{version}.tar.gz

BuildRequires:  golang >= 1.24
Requires:       (docker-ce or moby-engine)
Requires:       (docker-buildx-plugin or docker-buildx)
Recommends:     git

%description
Enclave provides isolated container environments for AI coding assistants and
development tools. It manages Docker containers with network isolation,
authentication injection, and tool-specific configurations.

%prep
%setup -q

%build
export GOCACHE="%{_builddir}/%{name}-%{version}/.gocache"
export GOMODCACHE="%{_builddir}/%{name}-%{version}/.gomodcache"
go mod vendor -modcacherw
mkdir -p bin completions
# Package-managed builds install the asset tree beside the executable. Disable
# cgo so RPMs built on Debian or Ubuntu do not acquire host glibc requirements.
CGO_ENABLED=0 go build -tags enclave_no_embed -mod=vendor \
    -ldflags "-X enclave/internal/buildinfo.Version=%{build_version} -X enclave/internal/buildinfo.Commit=%{build_commit} -X enclave/internal/buildinfo.Date=%{build_date}" \
    -o bin/enclave ./cmd/enclave
bin/enclave completion bash > completions/enclave

%install
app_root="%{buildroot}%{_datadir}/%{name}"
doc_root="%{buildroot}%{_datadir}/doc/%{name}"

install -D -m 0755 bin/enclave "$app_root/enclave"
mkdir -p "%{buildroot}%{_bindir}"
ln -s ../share/%{name}/enclave "%{buildroot}%{_bindir}/enclave"

install -D -m 0644 Dockerfile "$app_root/Dockerfile"
install -D -m 0644 Dockerfile.gateway "$app_root/Dockerfile.gateway"
install -D -m 0755 entrypoint.sh "$app_root/entrypoint.sh"
install -D -m 0755 gateway-entrypoint.sh "$app_root/gateway-entrypoint.sh"
install -D -m 0644 .dockerignore "$app_root/.dockerignore"

mkdir -p "$app_root/docs"
cp -a docs/. "$app_root/docs/"
chmod -R u+rwX,go+rX "$app_root/docs"

# Keep executable-asset rules in sync with Dockerfile, debian/rules,
# internal/appassets, internal/app/build_permissions.go, and
# internal/app/dockerfile_gen.go.
mkdir -p "$app_root/extensions"
cp -a extensions/tools extensions/features "$app_root/extensions/"
chmod -R u+rwX,go+rX "$app_root/extensions"
find "$app_root/extensions" -type f -name install.sh -exec chmod a+rx {} +

mkdir -p "$app_root/runtime-assets"
cp -a runtime-assets/. "$app_root/runtime-assets/"
chmod -R u+rwX,go+rX "$app_root/runtime-assets"
find "$app_root/runtime-assets/build-scripts" -type f \( -name '*.sh' -o -path '*/bin/*' \) -exec chmod a+rx {} +

# Install gateway proxy Go sources required by Dockerfile.gateway.
while IFS= read -r path; do
    case "$path" in ""|\#*) continue ;; esac
    dest="$app_root/$(dirname "$path")"
    mkdir -p "$dest"
    cp -a "$path" "$dest/"
done < internal/gateway/gateway_proxy_build_inputs.txt

install -D -m 0644 completions/enclave \
    "%{buildroot}%{_datadir}/bash-completion/completions/enclave"

install -D -m 0644 README.md "$doc_root/README.md"
install -D -m 0644 docs/ARCHITECTURE.md "$doc_root/ARCHITECTURE.md"
install -D -m 0644 docs/extensions/README.md "$doc_root/EXTENSIONS.md"
install -D -m 0644 LICENSE.md "$doc_root/LICENSE.md"
install -D -m 0644 NOTICE.md "$doc_root/NOTICE.md"

%clean
for cache in .gocache .gomodcache; do
    if [ -d "$cache" ]; then
        chmod -R u+w "$cache"
    fi
done
rm -rf "%{buildroot}"

%files
%{_bindir}/enclave
%{_datadir}/%{name}
%{_datadir}/bash-completion/completions/enclave
%dir %{_datadir}/doc/%{name}
%doc %{_datadir}/doc/%{name}/README.md
%doc %{_datadir}/doc/%{name}/ARCHITECTURE.md
%doc %{_datadir}/doc/%{name}/EXTENSIONS.md
%license %{_datadir}/doc/%{name}/LICENSE.md
%license %{_datadir}/doc/%{name}/NOTICE.md

%changelog
* Thu Jan 29 2026 Olaf Lessenich <olessenich@eclipsesource.com> - %{version}-%{release}
- Initial release.
