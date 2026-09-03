<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		createAnalysisProfile,
		deleteAnalysisProfile,
		getPipelineAnalysisSettings,
		listAnalysisProfiles,
		listAnalysisProviders,
		savePipelineAnalysisSettings,
		testAnalysisProvider,
		updateAnalysisProfile,
		type AnalysisProfile,
		type AnalysisProvider,
		type AnalysisProviderTestRequest
	} from '$lib/api/client';
	import {
		STAGES,
		STAGE_LABELS,
		blankProfileDraft,
		canonicalWireOptions,
		defaultStageOptions,
		modelChoices,
		profileDraftComplete,
		profileNameError,
		profileRequestFromDraft,
		providerTestErrorText,
		stageLabel,
		supportedEfforts,
		type ProfileDraft,
		type StageID
	} from '$lib/settings/analysisProfiles';

	let loading = $state(true);
	let loadError = $state('');
	let providers = $state<AnalysisProvider[]>([]);
	let profiles = $state<AnalysisProfile[]>([]);
	let activeProfileID = $state('');
	let editorOpen = $state(false);
	let editingProfile = $state<AnalysisProfile | null>(null);
	let draft = $state<ProfileDraft>(blankProfileDraft());
	let saving = $state(false);
	let saveError = $state('');
	let activationBusy = $state('');
	let testingProvider = $state('');
	let testResult = $state<{ id: string; status: string; duration_ms: number; error_code?: string } | null>(null);

	onMount(() => void loadAll());

	const providersByID = $derived(new Map(providers.map((provider) => [provider.id, provider])));
	const enabledProviders = $derived(providers.filter((provider) => provider.enabled));
	const canSave = $derived(profileDraftComplete(draft) && !saving);

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}

	async function loadAll(): Promise<void> {
		loading = true;
		loadError = '';
		const [providerResult, profileResult, settingsResult] = await Promise.allSettled([
			listAnalysisProviders(),
			listAnalysisProfiles(),
			getPipelineAnalysisSettings()
		]);
		if (providerResult.status === 'fulfilled') providers = providerResult.value.providers;
		else loadError = errorMessage(providerResult.reason, 'Could not load analysis providers.');
		if (profileResult.status === 'fulfilled') profiles = profileResult.value.profiles;
		else if (!loadError) loadError = errorMessage(profileResult.reason, 'Could not load analysis profiles.');
		if (settingsResult.status === 'fulfilled') activeProfileID = settingsResult.value.active_profile_id ?? '';
		else if (!loadError) loadError = errorMessage(settingsResult.reason, 'Could not load the active profile.');
		loading = false;
	}

	function bindingSummary(profile: AnalysisProfile): string {
		return profile.bindings
			.map((binding) => `${stageLabel(binding.stage_id)}: ${providersByID.get(binding.provider_id)?.label ?? binding.provider_id} · ${binding.model_id}`)
			.join(' · ');
	}

	async function chooseActive(profileId: string): Promise<void> {
		activationBusy = profileId;
		saveError = '';
		try {
			const result = await savePipelineAnalysisSettings({ active_profile_id: profileId });
			activeProfileID = result.active_profile_id;
		} catch (cause) {
			saveError = errorMessage(cause, 'Could not activate the profile.');
		} finally {
			activationBusy = '';
		}
	}

	function openNewProfile(): void {
		editingProfile = null;
		draft = blankProfileDraft();
		editorOpen = true;
		saveError = '';
	}

	function openProfileEditor(profile: AnalysisProfile): void {
		editingProfile = profile;
		draft = blankProfileDraft();
		draft.name = profile.name;
		for (const binding of profile.bindings) {
			if (!STAGES.includes(binding.stage_id as StageID)) continue;
			const stage = binding.stage_id as StageID;
			const provider = providersByID.get(binding.provider_id);
			draft.stages[stage] = {
				stage_id: stage,
				provider_id: binding.provider_id,
				provider_type: provider?.type ?? '',
				model_id: binding.model_id,
				options: { ...binding.options }
			};
		}
		editorOpen = true;
		saveError = '';
	}

	function closeEditor(): void {
		editorOpen = false;
		editingProfile = null;
	}

	function assignProvider(stage: StageID, providerId: string): void {
		const provider = providersByID.get(providerId);
		const stageDraft = draft.stages[stage];
		stageDraft.provider_id = providerId;
		stageDraft.provider_type = provider?.type ?? stageDraft.provider_type;
		stageDraft.options = defaultStageOptions(provider?.type ?? stageDraft.provider_type);
		const choices = provider ? modelChoices(provider) : [];
		stageDraft.model_id = choices[0] ?? stageDraft.model_id;
	}

	async function runProviderTest(provider: AnalysisProvider): Promise<void> {
		testingProvider = provider.id;
		testResult = null;
		try {
			const model = modelChoices(provider)[0];
			if (!model) throw new Error('This provider has no model catalog to test with.');
			const request: AnalysisProviderTestRequest = {
				stage_id: 'linguistic_analysis',
				model_id: model,
				options: canonicalWireOptions({
					stage_id: 'linguistic_analysis',
					provider_id: provider.id,
					provider_type: provider.type,
					model_id: model,
					options: defaultStageOptions(provider.type)
				})
			};
			const result = await testAnalysisProvider(provider.id, request);
			testResult = { id: provider.id, status: result.status, duration_ms: result.duration_ms, error_code: result.error_code };
		} catch (cause) {
			testResult = { id: provider.id, status: 'unhealthy', duration_ms: 0, error_code: cause instanceof DoublanguAPIError ? cause.code : '' };
		} finally {
			testingProvider = '';
		}
	}

	async function saveProfile(): Promise<void> {
		if (!canSave) return;
		saving = true;
		saveError = '';
		const payload = profileRequestFromDraft(draft);
		try {
			const saved = editingProfile
				? await updateAnalysisProfile(editingProfile.id, payload)
				: await createAnalysisProfile(payload);
			profiles = await (await listAnalysisProfiles()).profiles;
			editorOpen = false;
			editingProfile = null;
			if (!activeProfileID && profiles.some((profile) => profile.id === saved.id)) {
				await chooseActive(saved.id);
			}
			void loadAll();
		} catch (cause) {
			saveError = errorMessage(cause, 'Could not save the profile.');
		} finally {
			saving = false;
		}
	}

	async function removeProfile(profile: AnalysisProfile): Promise<void> {
		if (profile.is_active || activationBusy) return;
		saveError = '';
		try {
			await deleteAnalysisProfile(profile.id);
			profiles = (await listAnalysisProfiles()).profiles;
		} catch (cause) {
			saveError = errorMessage(cause, 'Could not delete the profile.');
		}
	}

	function nameIssue(): string {
		return profileNameError(draft.name);
	}
</script>

<section class="panel pipeline-panel" aria-labelledby="pipeline-heading">
	<div class="section-heading">
		<div>
			<h2 id="pipeline-heading">Analysis providers &amp; profiles</h2>
			<p class="muted">Create, activate, and test provider profiles. Endpoints and secrets stay on the server.</p>
		</div>
		{#if !loading}
			<button type="button" class="secondary" onclick={() => void loadAll()}>Refresh</button>
		{/if}
	</div>

	{#if loading}
		<p class="status" role="status">Loading providers and profiles…</p>
	{:else if loadError}
		<p class="error-text" role="alert">{loadError}</p>
	{:else if providers.length === 0}
		<p class="muted" role="status">No pipeline providers are configured on this server; the legacy model selection above is in use.</p>
	{:else}
		{#if saveError}<p class="error-text" role="alert">{saveError}</p>{/if}

		<h3 class="sub-heading">Providers</h3>
		<ul class="provider-list" role="list">
			{#each providers as provider (provider.id)}
				<li class="provider-card" class:disabled={!provider.enabled}>
					<div class="provider-main">
						<strong>{provider.label ?? provider.id}</strong>
						<span class="muted">{provider.type} · {provider.health}</span>
					</div>
					{#if provider.enabled}
						<button
							type="button"
							class="secondary test-button"
							disabled={testingProvider === provider.id}
							onclick={() => void runProviderTest(provider)}
						>
							{testingProvider === provider.id ? 'Testing…' : 'Test provider'}
						</button>
					{/if}
				</li>
				{#if testResult && testResult.id === provider.id}
					<li class="test-result" role="status">
						{testResult.status === 'healthy' ? 'Conformance fixture passed' : providerTestErrorText(testResult.error_code)}
						{testResult.duration_ms > 0 ? ` in ${testResult.duration_ms} ms` : ''}
					</li>
				{/if}
			{/each}
		</ul>

		<div class="profiles-heading">
			<h3 class="sub-heading">Profiles</h3>
			<button type="button" class="secondary" onclick={openNewProfile}>New profile</button>
		</div>
		{#if profiles.length === 0}
			<p class="muted">No profiles yet. Create one to start analyzing through the pipeline.</p>
		{:else}
			<ul class="profile-list" role="list">
				{#each profiles as profile (profile.id)}
					<li class="profile-row" class:active={profile.is_active || profile.id === activeProfileID}>
						<label class="profile-activate">
							<input
								type="radio"
								name="active-profile"
								checked={profile.is_active || profile.id === activeProfileID}
								disabled={activationBusy !== ''}
								onchange={() => void chooseActive(profile.id)}
							/>
							<span class="profile-copy">
								<strong>{profile.name}</strong>
								<small class="muted">{bindingSummary(profile)}</small>
							</span>
						</label>
						<div class="profile-actions">
							{#if activationBusy === profile.id}
								<span class="muted" role="status">Activating…</span>
							{/if}
							<button type="button" class="secondary" onclick={() => openProfileEditor(profile)}>Edit</button>
							<button
								type="button"
								class="secondary danger"
								disabled={profile.is_active || profile.id === activeProfileID}
								onclick={() => void removeProfile(profile)}
							>
								Delete
							</button>
						</div>
					</li>
				{/each}
			</ul>
		{/if}

		{#if editorOpen}
			<div class="profile-editor" role="group" aria-label={editingProfile ? 'Edit profile' : 'New profile'}>
				<div class="editor-heading">
					<h3>{editingProfile ? `Edit ${editingProfile.name}` : 'New profile'}</h3>
					<button type="button" class="secondary" onclick={closeEditor}>Cancel</button>
				</div>
				<label class="field">
					<span>Profile name</span>
					<input type="text" maxlength="120" bind:value={draft.name} placeholder="e.g. Mixed codex + omlx" />
					{#if draft.name && nameIssue()}<small class="error-text">{nameIssue()}</small>{/if}
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
								<select value={stageDraft.provider_id} onchange={(event) => assignProvider(stage, event.currentTarget.value)}>
									<option value="">Select a provider</option>
									{#each enabledProviders as provider (provider.id)}
										<option value={provider.id}>{provider.label ?? provider.id}</option>
									{/each}
								</select>
							</label>
							<label class="field">
								<span>Model</span>
								<input
									type="text"
									list={`models-${stage}`}
									bind:value={stageDraft.model_id}
									placeholder={selectedProvider ? 'Type a model id…' : 'Select a provider first'}
									disabled={!stageDraft.provider_id}
								/>
								{#if selectedProvider}
									<datalist id={`models-${stage}`}>
										{#each modelChoices(selectedProvider) as modelId (modelId)}
											<option value={modelId}></option>
										{/each}
									</datalist>
								{/if}
							</label>
						</div>
						{#if stageDraft.provider_type === 'openai_compatible'}
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
					<button type="button" class="primary" disabled={!canSave} onclick={() => void saveProfile()}>
						{saving ? 'Saving…' : editingProfile ? 'Save changes' : 'Create profile'}
					</button>
					{#if !canSave && draft.name}
						<span class="muted" role="status">Both stages need a provider and a model before saving.</span>
					{/if}
				</div>
			</div>
		{/if}
	{/if}
</section>

<style>
	.pipeline-panel { margin-top: 1.25rem; }
	.section-heading, .profiles-heading, .editor-heading { display: flex; align-items: start; justify-content: space-between; gap: 0.75rem; }
	.section-heading p { margin: 0.2rem 0 0; }
	.muted, .status { color: var(--color-muted); }
	.error-text { color: var(--color-danger); }
	.sub-heading { margin: 1.25rem 0 0.6rem; }
	.provider-list, .profile-list { list-style: none; margin: 0 0 0.4rem; padding: 0; display: grid; gap: 0.55rem; }
	.provider-card, .profile-row { display: flex; align-items: center; justify-content: space-between; gap: 0.9rem; padding: 0.7rem 0.85rem; border: 1px solid var(--color-border); border-radius: 0.55rem; }
	.provider-main { display: grid; gap: 0.15rem; }
	.provider-card.disabled { opacity: 0.55; }
	.test-result { list-style: none; font-size: 0.88rem; color: var(--color-muted); }
	.profile-row.active { border-color: var(--color-accent); }
	.profile-activate { display: flex; gap: 0.65rem; align-items: start; cursor: pointer; }
	.profile-copy { display: grid; gap: 0.2rem; }
	.profile-copy small { overflow-wrap: anywhere; }
	.profile-actions { display: flex; gap: 0.45rem; align-items: center; }
	.primary, .secondary { border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 0.45rem 0.7rem; cursor: pointer; font: inherit; }
	.primary { background: var(--color-accent); color: #171325; border-color: transparent; font-weight: 700; }
	.secondary { background: var(--color-surface-raised); color: var(--color-text); }
	.danger { color: var(--color-danger); }
	button:disabled { opacity: 0.5; cursor: not-allowed; }
	.test-button { white-space: nowrap; }
	.profile-editor { margin-top: 1rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.6rem; background: var(--color-surface-raised); display: grid; gap: 0.9rem; }
	.binding-editor { border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 0.7rem 0.8rem 0.85rem; display: grid; gap: 0.7rem; margin: 0; }
	.binding-fields { display: grid; grid-template-columns: 1fr 1fr; gap: 0.7rem; }
	.field { display: grid; gap: 0.3rem; font-size: 0.88rem; }
	.field > span { font-weight: 650; }
	.field input, .field select { width: 100%; padding: 0.45rem 0.55rem; font: inherit; }
	.editor-actions { display: flex; align-items: center; gap: 0.8rem; }
	@media (max-width: 600px) {
		.profile-row { align-items: start; flex-direction: column; }
		.binding-fields { grid-template-columns: 1fr; }
		.editor-actions { align-items: start; flex-direction: column; }
	}
</style>
