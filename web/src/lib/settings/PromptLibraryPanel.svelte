<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		listPromptVersions,
		savePromptVersion,
		type AnalysisPromptType,
		type AnalysisPromptVersion
	} from '$lib/api/client';
	import { PROMPT_LABELS, PROMPT_TYPES } from '$lib/settings/analysisProfiles';

	interface Props {
		/** Called after a successful save with the submitted type and version. */
		onsaved?: (promptType: AnalysisPromptType, version: AnalysisPromptVersion) => void;
	}
	let { onsaved }: Props = $props();

	// Saved versions per prompt type, the currently viewed version per type,
	// and the explicit "edit as new version" draft. Saving always creates a
	// new immutable version and never changes any profile's pinned selection.
	let versions = $state<Record<string, AnalysisPromptVersion[]>>({});
	let viewing = $state<Record<string, string>>({});
	let selectedType = $state<AnalysisPromptType>('explore');
	let editing = $state(false);
	let draftInstruction = $state('');
	let draftLabel = $state('');
	let loading = $state(true);
	let loadError = $state('');
	let saving = $state(false);
	let saveError = $state('');
	let saveSuccess = $state('');

	onMount(() => void loadAll());

	async function loadAll(): Promise<void> {
		loading = true;
		loadError = '';
		const next: Record<string, AnalysisPromptVersion[]> = {};
		try {
			await Promise.all(
				PROMPT_TYPES.map(async (promptType) => {
					const response = await listPromptVersions(promptType);
					next[promptType] = response.versions;
					viewing[promptType] = response.versions[0]?.id ?? '';
				})
			);
		} catch (cause) {
			loadError = errorMessage(cause, 'Could not load saved prompt versions.');
		} finally {
			versions = next;
			loading = false;
		}
	}

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}

	function selectedVersion(): AnalysisPromptVersion | undefined {
		return (versions[selectedType] ?? []).find((version) => version.id === viewing[selectedType]);
	}

	function startNewVersion(): void {
		const current = selectedVersion();
		draftInstruction = current?.instruction_text ?? '';
		draftLabel = current?.label ?? '';
		saveError = '';
		saveSuccess = '';
		editing = true;
	}

	function cancelEditing(): void {
		editing = false;
		draftInstruction = '';
		draftLabel = '';
		saveError = '';
	}

	async function saveNewVersion(): Promise<void> {
		if (saving || draftInstruction.trim() === '') return;
		// Capture the submitted type before awaiting: the selectors are
		// disabled while saving, so a completion can never file the version
		// under a different type or clear a different draft.
		const submittedType = selectedType;
		const submittedInstruction = draftInstruction;
		const submittedLabel = draftLabel;
		saving = true;
		saveError = '';
		try {
			const response = await savePromptVersion(submittedType, {
				instruction_text: submittedInstruction,
				label: submittedLabel
			});
			versions[submittedType] = [response.version, ...(versions[submittedType] ?? [])];
			viewing[submittedType] = response.version.id;
			saveSuccess = `Saved v${response.version.version} for ${PROMPT_LABELS[submittedType]}. Pin it on a profile to use it; nothing was activated.`;
			editing = false;
			draftInstruction = '';
			draftLabel = '';
			onsaved?.(submittedType, response.version);
		} catch (cause) {
			// Validation and network failures keep the submitted draft on screen.
			saveError = errorMessage(cause, 'Could not save the prompt version.');
		} finally {
			saving = false;
		}
	}
</script>

<section class="prompt-library" aria-labelledby="prompt-library-heading">
	<div class="library-heading">
		<h2 id="prompt-library-heading">Prompts</h2>
		<p class="muted library-intro">
			Saved prompt versions are immutable and shared by every profile. Pin one per prompt type inside each profile.
		</p>
	</div>

	<div class="library-fields">
		<label class="field">
			<span>Prompt type</span>
			<select bind:value={selectedType} disabled={saving} onchange={cancelEditing}>
				{#each PROMPT_TYPES as promptType (promptType)}
					<option value={promptType}>{PROMPT_LABELS[promptType]}</option>
				{/each}
			</select>
		</label>
		<label class="field">
			<span>Saved version</span>
			<select bind:value={viewing[selectedType]} disabled={saving} onchange={cancelEditing}>
				{#each versions[selectedType] ?? [] as version (version.id)}
					<option value={version.id}>v{version.version}{version.label ? ` · ${version.label}` : ''} · {version.created_at}</option>
				{/each}
			</select>
		</label>
	</div>

	{#if loading}
		<p class="muted" role="status">Loading saved prompts…</p>
	{:else if loadError}
		<p class="error-text" role="alert">{loadError}</p>
	{:else}
		{#if saveSuccess}
			<p class="status" role="status">{saveSuccess}</p>
		{/if}
		{#if saveError}
			<p class="error-text" role="alert">{saveError}</p>
		{/if}
		{#if editing}
			<div class="editor" role="group" aria-label="New prompt version">
				<label class="field">
					<span>Instruction text (saved as a new version)</span>
					<textarea rows="10" bind:value={draftInstruction} disabled={saving}></textarea>
				</label>
				<label class="field">
					<span>Label (optional)</span>
					<input type="text" maxlength="80" bind:value={draftLabel} placeholder="e.g. More concise wording" disabled={saving} />
				</label>
				<div class="editor-actions">
					<button type="button" class="primary" disabled={saving || draftInstruction.trim() === ''} onclick={() => void saveNewVersion()}>
						{saving ? 'Saving…' : 'Save new version'}
					</button>
					<button type="button" class="secondary" disabled={saving} onclick={cancelEditing}>Cancel</button>
					{#if draftInstruction.trim() === ''}
						<span class="muted" role="status">Instruction text is required.</span>
					{/if}
				</div>
			</div>
		{:else}
			<div class="saved-text">
				<pre>{selectedVersion()?.instruction_text ?? 'No saved version selected.'}</pre>
			</div>
			<div class="editor-actions">
				<button type="button" class="secondary" disabled={saving || !selectedVersion()} onclick={startNewVersion}>
					Edit as new version
				</button>
			</div>
		{/if}
	{/if}
</section>

<style>
	.prompt-library {
		display: grid;
		gap: 0.8rem;
	}

	.library-heading h2 {
		margin-bottom: 0.35rem;
	}

	.library-intro {
		margin: 0;
	}

	.library-fields {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.7rem;
	}

	.field {
		display: grid;
		gap: 0.3rem;
		font-size: 0.88rem;
	}

	.field > span {
		font-weight: 650;
	}

	.field input,
	.field select,
	.field textarea {
		width: 100%;
		padding: 0.45rem 0.55rem;
		font: inherit;
	}

	.muted {
		color: var(--color-muted);
	}

	.status {
		color: var(--color-accent);
	}

	.error-text {
		color: var(--color-danger);
	}

	.saved-text pre {
		margin: 0;
		padding: 0.75rem 0.85rem;
		border: 1px solid var(--color-border);
		border-radius: 0.55rem;
		background: var(--color-surface-raised);
		overflow-wrap: anywhere;
		white-space: pre-wrap;
		font-size: 0.85rem;
	}

	.primary,
	.secondary {
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.45rem 0.7rem;
		cursor: pointer;
		font: inherit;
	}

	.primary {
		background: var(--color-accent);
		color: #171325;
		border-color: transparent;
		font-weight: 700;
	}

	.secondary {
		background: var(--color-surface-raised);
		color: var(--color-text);
	}

	button:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.editor {
		display: grid;
		gap: 0.7rem;
		padding: 0.85rem;
		border: 1px solid var(--color-border);
		border-radius: 0.6rem;
		background: var(--color-surface-raised);
	}

	.editor-actions {
		display: flex;
		align-items: center;
		gap: 0.8rem;
		flex-wrap: wrap;
	}

	@media (max-width: 600px) {
		.library-fields {
			grid-template-columns: 1fr;
		}

		.editor-actions {
			align-items: start;
			flex-direction: column;
		}
	}
</style>
