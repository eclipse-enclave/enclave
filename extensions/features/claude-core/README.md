# Claude Core

Installs Claude Code and `@anthropic-ai/sandbox-runtime`. Declares the Anthropic
API key, Claude Code OAuth token, and Anthropic network access once for consumers.

Claude, Theia, and Theia Next require this feature. It is otherwise opt-in.
Launch behavior, providers, settings, OAuth files, and writable stores belong to
individual tools. Credential lookup continues to use the current tool's host
credential layers; this feature does not share writable authentication stores.
