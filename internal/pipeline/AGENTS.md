# internal/pipeline

Owns the fixed two-stage analysis pipeline identities and provider-neutral
profile snapshots. There remain exactly two article analysis stages;
Explore and sentence translation are separate on-demand operations that reuse
the executor, never extra stages.

Rules:

- This package imports no annotator, analysis, reader, or semantics package.
  Every other layer may rely on it without creating an import cycle.
- Stage IDs, contract/prompt versions, and the registered stage order are
  code-defined here; the database stores plain text stage columns.
- Profile bindings are provider-neutral snapshots: they contain no endpoint,
  credential, or secret.
- Hash functions are domain-separated and deterministic over canonical JSON.
- Prompt snapshots capture exact owner-selected instruction versions; the
  captured envelope contract is versioned and unsupported versions are
  rejected rather than silently re-rendered.
