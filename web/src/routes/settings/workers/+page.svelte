<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		createSpeechWorkerEnrollment,
		listSpeechWorkers,
		revokeSpeechWorker,
		type SpeechWorker,
		type WorkerEnrollment
	} from '$lib/api/client';

	let workers = $state<SpeechWorker[]>([]);
	let loading = $state(true);
	let listError = $state('');

	let enrolling = $state(false);
	let enrollError = $state('');
	// The plaintext token lives only in this component's memory: never in
	// storage, URLs, or logs. Generating a new token replaces the shown one.
	let enrollment = $state<WorkerEnrollment | null>(null);
	let copyState = $state<'' | 'copied' | 'error'>('');

	let revokeTarget = $state<SpeechWorker | null>(null);
	let revokingId = $state('');
	let revokeErrors = $state<Record<string, string>>({});

	onMount(() => {
		void loadWorkers();
	});

	async function loadWorkers() {
		loading = true;
		listError = '';
		try {
			workers = await listSpeechWorkers();
		} catch (cause) {
			listError = errorMessage(cause, 'Could not load workers.');
		} finally {
			loading = false;
		}
	}

	async function generateEnrollment() {
		if (enrolling) return;
		enrolling = true;
		enrollError = '';
		copyState = '';
		try {
			enrollment = await createSpeechWorkerEnrollment();
		} catch (cause) {
			enrollError = errorMessage(cause, 'Could not generate an enrollment token.');
		} finally {
			enrolling = false;
		}
	}

	async function copyToken() {
		if (!enrollment) return;
		try {
			if (!navigator.clipboard?.writeText) throw new Error('Clipboard is unavailable.');
			await navigator.clipboard.writeText(enrollment.token);
			copyState = 'copied';
		} catch {
			copyState = 'error';
		}
	}

	function askRevoke(worker: SpeechWorker) {
		revokeErrors = {};
		revokeTarget = worker;
	}

	function cancelRevoke() {
		revokeTarget = null;
	}

	async function confirmRevoke() {
		const worker = revokeTarget;
		if (!worker || revokingId) return;
		revokingId = worker.id;
		delete revokeErrors[worker.id];
		try {
			await revokeSpeechWorker(worker.id);
			revokeTarget = null;
			await loadWorkers();
		} catch (cause) {
			revokeErrors[worker.id] = errorMessage(cause, `Could not revoke ${worker.name}.`);
		} finally {
			revokingId = '';
		}
	}

	function isRevoked(worker: SpeechWorker): boolean {
		return Boolean(worker.revoked_at);
	}

	function capabilityText(worker: SpeechWorker): string {
		const parts = (worker.capabilities ?? []).map((cap) => {
			const languages = cap.languages?.length > 0 ? ` (${cap.languages.join(', ')})` : '';
			return `TTS ${cap.engine}${languages}`;
		});
		if ((worker.llm_relay_capabilities ?? []).length > 0) parts.push('LLM relay');
		return parts.length > 0 ? parts.join(' · ') : '—';
	}

	function relativeTime(iso: string): string {
		if (!iso) return '—';
		const then = new Date(iso).getTime();
		if (Number.isNaN(then)) return iso;
		const seconds = Math.round((Date.now() - then) / 1000);
		if (seconds < 60) return 'just now';
		if (seconds < 3600) {
			const minutes = Math.floor(seconds / 60);
			return `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
		}
		if (seconds < 86400) {
			const hours = Math.floor(seconds / 3600);
			return `${hours} hour${hours === 1 ? '' : 's'} ago`;
		}
		return formatDate(iso);
	}

	function formatDate(iso: string): string {
		const date = new Date(iso);
		if (Number.isNaN(date.getTime())) return iso;
		return date.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
	}

	function formatDateTime(iso: string): string {
		const date = new Date(iso);
		if (Number.isNaN(date.getTime())) return iso;
		return date.toLocaleString(undefined, { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
	}

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}
</script>

<svelte:head>
	<title>Workers — Doublangu</title>
</svelte:head>

<section class="workers-intro" aria-labelledby="workers-heading">
	<h2 id="workers-heading">Workers</h2>
	<p class="intro">
		Workers connect to Doublangu to process speech and relay jobs. Enroll a new worker here or revoke a worker that should no
		longer connect.
	</p>
</section>

<section class="enrollment" aria-labelledby="enroll-heading">
	<h2 id="enroll-heading">Enroll a new worker</h2>
	<p class="muted enroll-help">Generate a one-time enrollment token for a worker. The token expires after 30 minutes and can be used once.</p>
	<button type="button" class="secondary" disabled={enrolling} onclick={() => void generateEnrollment()}>
		{enrolling ? 'Generating…' : 'Generate enrollment token'}
	</button>
	{#if enrollError}<p class="error-text" role="alert">{enrollError}</p>{/if}
	{#if enrollment}
		<div class="token-card">
			<h3>Enrollment token</h3>
			<code class="token-value">{enrollment.token}</code>
			<div class="token-actions">
				<button type="button" class="secondary" onclick={() => void copyToken()}>Copy token</button>
				{#if copyState === 'copied'}<span class="muted" role="status">Copied.</span>{/if}
				{#if copyState === 'error'}<span class="error-text" role="alert">Could not copy the token. Select it and copy it manually.</span>{/if}
			</div>
			<p class="muted">Expires: {formatDateTime(enrollment.expires_at)}</p>
			<p class="muted">Use this token in the worker setup. It can be used once.</p>
		</div>
	{/if}
</section>

<section class="workers-section" aria-labelledby="enrolled-heading">
	<h2 id="enrolled-heading">Enrolled workers</h2>
	{#if loading}
		<p class="status" role="status">Loading workers…</p>
	{:else if listError}
		<div class="error-box" role="alert">
			<p>{listError}</p>
			<button type="button" class="secondary" onclick={() => void loadWorkers()}>Retry</button>
		</div>
	{:else if workers.length === 0}
		<p class="muted">No workers are enrolled yet.</p>
	{:else}
		<ul class="worker-list" role="list">
			{#each workers as worker (worker.id)}
				<li class="worker-card" class:revoked={isRevoked(worker)}>
					<div class="worker-main">
						<div class="worker-title">
							<strong>{worker.name}</strong>
							{#if isRevoked(worker)}<span class="revoked-badge">Revoked</span>{/if}
						</div>
						<span class="muted">Version {worker.software_version} · {worker.protocol_version}</span>
						<span class="muted">Capabilities: {capabilityText(worker)}</span>
						<span class="muted">Last seen: {relativeTime(worker.last_seen_at)}</span>
						{#if worker.relay_last_seen_at}<span class="muted">Relay last seen: {relativeTime(worker.relay_last_seen_at)}</span>{/if}
						<span class="muted">Enrolled: {formatDate(worker.created_at)}</span>
					</div>
					{#if revokeErrors[worker.id]}<p class="error-text" role="alert">{revokeErrors[worker.id]}</p>{/if}
					{#if revokeTarget?.id === worker.id}
						<div class="confirm-box" role="group" aria-label={`Confirm revoking ${worker.name}`}>
							<p>Revoke {worker.name}? This worker will no longer be able to connect.</p>
							<div class="confirm-actions">
								<button type="button" class="secondary" onclick={cancelRevoke}>Cancel</button>
								<button type="button" class="secondary danger" disabled={revokingId !== ''} onclick={() => void confirmRevoke()}>
									{revokingId === worker.id ? 'Revoking…' : 'Revoke worker'}
								</button>
							</div>
						</div>
					{:else if !isRevoked(worker)}
						<div class="worker-actions">
							<button type="button" class="secondary danger" onclick={() => askRevoke(worker)}>Revoke</button>
						</div>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	h2 {
		margin-bottom: 0.35rem;
	}

	.workers-intro .intro {
		margin: 0 0 1.5rem;
		color: var(--color-muted);
	}

	.enrollment {
		margin-bottom: 1.75rem;
	}

	.enroll-help {
		margin: 0 0 0.75rem;
	}

	.muted,
	.status {
		color: var(--color-muted);
	}

	.error-text {
		color: var(--color-danger);
	}

	.error-box {
		padding: 0.85rem;
		border-radius: 0.55rem;
		background: var(--color-danger-bg);
		color: var(--color-danger);
	}

	.error-box p {
		margin-bottom: 0.7rem;
	}

	.secondary {
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.45rem 0.7rem;
		cursor: pointer;
		font: inherit;
		background: var(--color-surface-raised);
		color: var(--color-text);
	}

	.danger {
		color: var(--color-danger);
	}

	button:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.token-card {
		margin-top: 0.9rem;
		padding: 0.9rem 1rem;
		border: 1px solid var(--color-accent);
		border-radius: 0.6rem;
		background: var(--color-surface-raised);
		display: grid;
		gap: 0.5rem;
	}

	.token-card h3 {
		margin: 0;
	}

	.token-value {
		padding: 0.6rem 0.7rem;
		border: 1px solid var(--color-border);
		border-radius: 0.45rem;
		background: var(--color-surface);
		font: 0.85rem/1.4 ui-monospace, SFMono-Regular, Menlo, monospace;
		overflow-wrap: anywhere;
		user-select: all;
	}

	.token-actions {
		display: flex;
		align-items: center;
		gap: 0.7rem;
	}

	.token-card p {
		margin: 0;
		font-size: 0.9rem;
	}

	.worker-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.55rem;
	}

	.worker-card {
		display: grid;
		gap: 0.55rem;
		padding: 0.7rem 0.85rem;
		border: 1px solid var(--color-border);
		border-radius: 0.55rem;
	}

	.worker-card.revoked {
		opacity: 0.6;
	}

	.worker-main {
		display: grid;
		gap: 0.15rem;
	}

	.worker-title {
		display: flex;
		align-items: baseline;
		gap: 0.6rem;
	}

	.revoked-badge {
		font-size: 0.8rem;
		font-weight: 700;
		color: var(--color-danger);
	}

	.worker-actions,
	.confirm-actions {
		display: flex;
		gap: 0.45rem;
	}

	.confirm-box {
		padding: 0.7rem 0.8rem;
		border: 1px solid var(--color-danger);
		border-radius: 0.5rem;
		background: var(--color-danger-bg);
	}

	.confirm-box p {
		margin: 0 0 0.6rem;
		color: var(--color-text);
	}
</style>
