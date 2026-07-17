// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import "fmt"

// warnReservedFields emits one warning per sbx field that enclave parses
// but does not yet honor. The field shapes are reserved so behavior can be
// added later without a surface change.
func warnReservedFields(doc specDocument, name string, warn func(string)) {
	w := func(field, detail string) {
		warn(fmt.Sprintf("extension %q: %s is reserved but not yet honored%s", name, field, detail))
	}
	if doc.AgentContext != "" {
		w("agentContext", "; it will be ignored")
	}
	// aiFilename names the memory file agentContext would be written into. Both
	// are intentionally deferred (no per-kit memory-file writer); warn on
	// aiFilename too so it is not a silent no-op while its partner warns.
	if doc.Sandbox != nil && doc.Sandbox.AIFilename != "" {
		w("sandbox.aiFilename", "; it will be ignored")
	}
	// sandbox.image is a hint only: enclave builds on its own base image and
	// will not swap it for the one a spec declares.
	if doc.Sandbox != nil && doc.Sandbox.Image != "" {
		w("sandbox.image", "; enclave keeps its own base image and will not swap it")
	}
	if doc.Commands != nil {
		// commands.install is honored for mixins (declarative install steps);
		// sandbox tools must use an install.sh sidecar, so warn only for them.
		if len(doc.Commands.Install) > 0 && doc.Kind == KindSandbox {
			w("commands.install", "; commands.install is honored for mixins only — tools should use an install.sh sidecar")
		}
	}
}
