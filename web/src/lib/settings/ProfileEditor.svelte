<script lang="ts">
	import type { AnalysisProvider } from '$lib/api/client';
	import {
		STAGES,
		STAGE_LABELS,
		modelChoices,
		supportedEfforts,
		usesNumericStageOptions,
		type ProfileDraft,
		type StageID
	} from '$lib/settings/analysisProfiles';

	/**
	 * The profile form extracted from AnalysisPipelinePanel so it can render
	 * directly inside the edited profile's list item (or below the New profile
	 * button). It is purely presentational: the panel owns the single draft,
	 * save flow, validation, and focus/discard decisions.
	 */
	interface ProfileEditorProps {
		draft: ProfileDraft;
		heading: string;
		ariaLabel: string;
		saveLabel: string;
		saving: boolean;
		canSave: boolean;
		saveError: string;
		blockedText: string;
		nameIssue: string;
		confirmMessage: string;
		enabledProviders: AnalysisProvider[];
		providersByID: Map<string, AnalysisProvider>;
		onassignprovider: (stage: StageID, providerId: string) => void;
		onchoosemodel: (stage: StageID, modelId: string) => void;
		onsave: () => void;
		oncancel: () => void;
		ondiscardswitch: () => void;
		onkeepediting: () => void;
	}

	let {
		draft,
		heading,
		ariaLabel,
		saveLabel,
		saving,
		canSave,
		saveError,
		blockedText,
		nameIssue,
		confirmMessage,
		enabledProviders,
		providersByID,
		onassignprovider,
		onchoosemodel,
		onsave,
		oncancel,
		ondiscardswitch,
		onkeepediting
	}: ProfileEditorProps = $props();

	/** Opening the editor must land keyboard users on the name field. */
	function autofocus(node: HTMLInputElement) {
		node.focus();
	}
</script>

<div class="profile-editor" role="group" aria-label={ariaLabel}>
	<div class="editor-heading">
		<h3>{heading}</h3>
		<button type="button" class="secondary" onclick={oncancel}>Cancel</button>
	</div>
	{#if confirmMessage}
		<div class="discard-confirm">
			<p role="alert">{confirmMessage}</p>
			<div class="confirm-actions">
				<button type="button" class="secondary danger" onclick={ondiscardswitch}>Discard changes</button>
				<button type="button" class="secondary" onclick={onkeepediting}>Keep editing</button>
			</div>
		</div>
	{/if}
	{#if saveError}<p class="error-text" role="alert">{saveError}</p>{/if}
	<label class="field">
		<span>Profile name</span>
		<input type="text" maxlength="80" bind:value={draft.name} placeholder="e.g. Mixed codex + omlx" use:autofocus />
		{#if draft.name && nameIssue}<small class="error-text">{nameIssue}</small>{/if}
	</label>

	{#each STAGES as stage (stage)}
		{@const stageDraft = draft.stages[stage]}
		{@const selectedProvider = providersByID.get(stageDraft.provider_id)}
		{@const stageEfforts = supportedEfforts(selectedProvider, stageDraft.model_id)}
		<fieldset class="binding-editor">
			<legend>{STAGE_LABELS[stage]}</legend>
			<div class="binding-fields">
				<label class="field">
					<span>Provider</span>
					<select value={stageDraft.provider_id} onchange={(event) => onassignprovider(stage, event.currentTarget.value)}>
						<option value="">Select a provider</option>
						{#each enabledProviders as provider (provider.id)}
							<option value={provider.id}>{provider.label ?? provider.id}</option>
						{/each}
					</select>
				</label>
				<label class="field">
					<span>Model</span>
					{#if selectedProvider && modelChoices(selectedProvider).length > 0}
						<select value={stageDraft.model_id} onchange={(event) => onchoosemodel(stage, event.currentTarget.value)}>
							{#each modelChoices(selectedProvider) as modelId (modelId)}
								<option value={modelId}>{modelId}</option>
							{/each}
						</select>
					{:else}
						<input
							type="text"
							bind:value={stageDraft.model_id}
							onchange={(event) => onchoosemodel(stage, event.currentTarget.value)}
							placeholder={selectedProvider ? 'Type a model id…' : 'Select a provider first'}
							disabled={!stageDraft.provider_id}
						/>
					{/if}
				</label>
			</div>
			{#if usesNumericStageOptions(stageDraft.provider_type)}
				<div class="binding-fields">
					<label class="field">
						<span>Temperature (milli)</span>
						<input type="number" min="0" max="2000" bind:value={stageDraft.options.temperature_milli} />
					</label>
					<label class="field">
						<span>Max output tokens</span>
						<input type="number" min="1024" max="65536" bind:value={stageDraft.options.max_output_tokens} />
					</label>
				</div>
			{:else}
				<label class="field">
					<span>Reasoning effort</span>
					{#if stageEfforts.length > 0}
						<select value={String(stageDraft.options.reasoning_effort ?? stageEfforts[0])} onchange={(event) => (stageDraft.options.reasoning_effort = event.currentTarget.value)}>
							{#each stageEfforts as effort (effort)}
								<option value={effort}>{effort}</option>
							{/each}
						</select>
					{:else}
						<input type="text" value={String(stageDraft.options.reasoning_effort ?? '')} disabled />
						<small class="muted">The catalog for this model lists no reasoning efforts; choose a model that advertises supported efforts.</small>
					{/if}
				</label>
			{/if}
		</fieldset>
	{/each}

	<div class="editor-actions">
		<button type="button" class="primary" disabled={!canSave} onclick={onsave}>
			{saving ? 'Saving…' : saveLabel}
		</button>
		{#if !canSave && draft.name && blockedText}
			<span class="muted" role="status">{blockedText}</span>
		{/if}
	</div>
</div>

<style>
	.profile-editor {
		padding: 1rem;
		border: 1px solid var(--color-border);
		border-radius: 0.6rem;
		background: var(--color-surface-raised);
		display: grid;
		gap: 0.9rem;
		width: 100%;
		box-sizing: border-box;
	}

	.editor-heading {
		display: flex;
		align-items: start;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.discard-confirm {
		border: 1px solid var(--color-warning);
		border-radius: 0.5rem;
		padding: 0.6rem 0.75rem;
		display: grid;
		gap: 0.55rem;
	}

	.discard-confirm p {
		margin: 0;
	}

	.confirm-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.error-text {
		color: var(--color-danger);
	}

	.muted {
		color: var(--color-muted);
	}

	.binding-editor {
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.7rem 0.8rem 0.85rem;
		display: grid;
		gap: 0.7rem;
		margin: 0;
	}

	.binding-fields {
		display: grid;
		grid-template-columns: 1fr 1fr;
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
	.field select {
		width: 100%;
		padding: 0.45rem 0.55rem;
		font: inherit;
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

	.danger {
		color: var(--color-danger);
	}

	button:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.editor-actions {
		display: flex;
		align-items: center;
		gap: 0.8rem;
	}

	@media (max-width: 600px) {
		.binding-fields {
			grid-template-columns: 1fr;
		}

		.editor-actions {
			align-items: start;
			flex-direction: column;
		}
	}
</style>
