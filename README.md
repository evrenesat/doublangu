# Doublangu Core And Language Learning Platform

Build Doublangu as a self-hosted, single-user language-learning media system with a small Go core, a Svelte 5/SvelteKit UI, native trusted Go plugins, trusted same-origin UI plugins, a Linux coordination/streaming server, and an outbound Mac processing agent. The first language pair is Dutch source content with English translation and explanations, but all core records and contracts use BCP-47 language tags and must support additional languages without schema changes.

The product imports audiobooks, ordinary audio, ebooks, text files, pasted text, and URLs; preserves source media; creates or aligns sentence-level text; translates it; detects grammatical constructions and multiword expressions; generates visual and audio explanations; synthesizes speech; composes passive-listening lesson sequences; and provides synchronized reading, editing, discovery, and playback on desktop, PWA, and Android.

Audiobookshelf and its Capacitor app are design references for library scanning, chapter-aware streaming, progress synchronization, offline downloads, and server-connected mobile behavior. They are not dependencies, forks, database sources, or API compatibility targets. The `tglide/svelte-radial-menu` project is an interaction reference only. Doublangu owns a new accessible, multilevel radial command system.

The native backend plugin mechanism intentionally uses Go's `plugin` package. Because native Go plugins require an exact toolchain, build configuration, and common dependency graph match, the server/agent and every native plugin in a release must be built together. Plugins are trusted and unsandboxed. Compatibility contracts provide modularity and deterministic behavior, not a security boundary.

```mermaid
flowchart LR
    Browser["Svelte PWA / Android shell"] -->|HTTPS + events| Server["Linux Go core"]
    Server --> DB["SQLite metadata"]
    Server --> Media["Filesystem media store"]
    Server --> Plugins["Linux Go plugins"]
    Agent["Outbound Mac agent"] -->|Authenticated WSS| Server
    Agent --> AgentPlugins["macOS Go plugins"]
    AgentPlugins --> LocalAI["Local STT / TTS / LLM"]
    Plugins --> CloudAI["Optional cloud providers"]
```

### Checkpoint And Commit Ownership

- Implementation agents must not stage or commit checkpoint work.
- The reviewer model owns checkpoint acceptance, staging, and commits.
- A later checkpoint must not begin until the reviewer has accepted and committed the prior checkpoint.
- The first accepted commit for each checkpoint uses `v01`. A correction after an existing checkpoint commit uses `v02`, then `v03`, and so on.
- The original plan remains the durable ledger under `plans/in-progress/` for the entire handoff.

### DevOps Ownership Boundary

Deployment work is explicitly outside this handoff. Implementing and reviewer agents must not edit the external `evreniops` repository, Ansible files, production nginx/systemd configuration, DNS, TLS, VPS state, cloud storage, or production secrets.

The project owner/architect will separately implement and operate Ansible deployment after the application produces the release artifacts and deployment contract defined in Checkpoint 15. Current hosting is a single Ubuntu 24.04 ARM64 VPS with 2 cores, 3.7 GiB RAM, no swap, and approximately 25 GiB free. It is suitable for the Go server and lightweight media operations, not AI inference or a large audiobook corpus. Production media storage therefore remains a configurable filesystem mount whose volume provisioning is an owner-operated decision.
