import type {
	AnalysisConformanceSummary,
	AnalysisProfile,
	AnalysisProfileBinding,
	AnalysisProfileBindingInput,
	AnalysisProfileInput,
	AnalysisProvider,
	AnalysisProviderTestRequest
} from '$lib/api/client';

export const STAGE_LINGUISTIC = 'linguistic_analysis';
export const STAGE_TRANSLATION = 'translation';
export const STAGES = [STAGE_LINGUISTIC, STAGE_TRANSLATION] as const;
export type StageID = (typeof STAGES)[number];

export interface StageDraft {
	stage_id: StageID;
	provider_id: string;
	provider_type: string;
	model_id: string;
	options: Record<string, unknown>;
}

export interface ProfileDraft {
	name: string;
	stages: Record<StageID, StageDraft>;
}

export const STAGE_LABELS: Record<StageID, string> = {
	[STAGE_LINGUISTIC]: 'Linguistic analysis',
	[STAGE_TRANSLATION]: 'Translation'
};

export function stageLabel(stageId: string): string {
	return STAGE_LABELS[stageId as StageID] ?? stageId;
}

export function isStageId(value: string): value is StageID {
	return STAGES.includes(value as StageID);
}

/** Default options for a provider type when the editor opens a new binding. */
export function defaultStageOptions(providerType: string): Record<string, unknown> {
	if (providerType === 'openai_compatible') {
		return { temperature_milli: 0, max_output_tokens: 16384 };
	}
	return { reasoning_effort: 'low' };
}

/** Reasoning efforts the selected model actually advertises in its catalog. */
export function supportedEfforts(provider: AnalysisProvider | undefined, modelId: string): string[] {
	if (!provider || !modelId) return [];
	const model = (provider.models ?? []).find((candidate) => candidate.id === modelId);
	if (!model) return [];
	return (model.supported_reasoning_efforts ?? []).map((effort) => effort.value);
}

/** Stable error-code text; never echoes raw provider or transport errors. */
export function providerTestErrorText(errorCode: string | undefined): string {
	if (!errorCode) return 'Test failed.';
	if (errorCode === 'v1.analysis_provider_unavailable') return 'Provider is unavailable.';
	if (errorCode === 'v1.analysis_stage_failed') return 'Stage rejected the fixture output.';
	if (errorCode === 'v1.validation_error') return 'Options are invalid for this provider.';
	return 'Test failed with a provider error.';
}

export function blankProfileDraft(): ProfileDraft {
	const stages = {} as Record<StageID, StageDraft>;
	for (const stage of STAGES) {
		stages[stage] = {
			stage_id: stage,
			provider_id: '',
			provider_type: 'codex_app_server',
			model_id: '',
			options: defaultStageOptions('codex_app_server')
		};
	}
	return { name: '', stages };
}

/** True when a draft is complete enough to save (both stages resolved). */
export function profileDraftComplete(draft: ProfileDraft): boolean {
	return draft.name.trim().length > 0 && STAGES.every((stage) => draft.stages[stage].provider_id !== '' && draft.stages[stage].model_id !== '');
}

/**
 * Owner-facing profile name rule, mirroring the server: trimmed, 1-80
 * Unicode scalar values, no C0 controls or DEL. Scalars are counted as code
 * points (not UTF-16 code units) so astral characters agree with the server.
 */
export function profileNameError(name: string): string {
	const trimmed = name.trim();
	if (!trimmed) return 'Name is required.';
	if ([...trimmed].length > 80) return 'Name must be 80 characters or fewer.';
	if (/[\x00-\x1F\x7F]/.test(trimmed)) return 'Name must not contain control characters.';
	return '';
}

/** Wire payload from a complete draft, with options canonicalized per type. */
export function profileRequestFromDraft(draft: ProfileDraft): AnalysisProfileInput {
	const bindings: AnalysisProfileBindingInput[] = STAGES.map((stage) => {
		const entry = draft.stages[stage];
		return {
			stage_id: stage,
			provider_id: entry.provider_id,
			model_id: entry.model_id,
			options: canonicalWireOptions(entry)
		};
	});
	return { name: draft.name.trim(), bindings };
}

export function canonicalWireOptions(entry: StageDraft): Record<string, never> {
	const options = { ...entry.options };
	if (entry.provider_type !== 'openai_compatible') {
		// The chosen effort is sent exactly as configured: the server
		// validates the model/effort pair against the provider catalog, so
		// the wire must never silently coerce an unsupported value into a
		// default that would change a saved profile.
		const effort = String(options.reasoning_effort ?? 'low').trim();
		options.reasoning_effort = effort || 'low';
	}
	// Numeric OMLX values travel exactly as configured: stageOptionsError
	// blocks invalid tuples client-side and the server strictly validates.
	return options as Record<string, never>;
}

/**
 * Issue blocking a stage's options object, or '' when the tuple is well
 * formed. OMLX temperature and max tokens must be integers in the server
 * ranges; a Codex binding needs an explicit effort (membership against the
 * model catalog is checked by bindingEffortError and the server).
 */
export function stageOptionsError(providerType: string, options: Record<string, unknown>): string {
	if (providerType === 'openai_compatible') {
		const temperature: unknown = options.temperature_milli;
		if (!Number.isInteger(temperature) || (temperature as number) < 0 || (temperature as number) > 2000) {
			return 'Temperature must be a whole number from 0 to 2000.';
		}
		const maxTokens: unknown = options.max_output_tokens;
		if (!Number.isInteger(maxTokens) || (maxTokens as number) < 1024 || (maxTokens as number) > 65536) {
			return 'Max output tokens must be a whole number from 1024 to 65536.';
		}
		return '';
	}
	if (!String(options.reasoning_effort ?? '').trim()) return 'Reasoning effort is required.';
	return '';
}

/** Model catalog choice for a provider; falls back to free-text model ids. */
export function modelChoices(provider: AnalysisProvider | undefined): string[] {
	const ids = (provider?.models ?? []).map((model) => model.id);
	return [...new Set(ids)];
}

/** One conformance test tuple: the exact stage/model/options sent to the test endpoint. */
export interface TestForm {
	model_id: string;
	options: Record<string, unknown>;
}

export function testFormKey(providerId: string, stage: StageID): string {
	return `${providerId}::${stage}`;
}

/** First reasoning effort a model advertises, or '' when the catalog lists none. */
export function firstAdvertisedEffort(provider: AnalysisProvider | undefined, modelId: string): string {
	return supportedEfforts(provider, modelId)[0] ?? '';
}

/**
 * Issue blocking a codex binding's effort, or '' when the binding is savable.
 * Incomplete bindings (no provider/model yet) and openai_compatible bindings
 * never report here; an effort the model does not advertise — including the
 * default when the catalog lists no efforts — must be fixed before saving.
 */
export function bindingEffortError(provider: AnalysisProvider | undefined, entry: StageDraft): string {
	if (!entry.provider_id || !entry.model_id) return '';
	if (entry.provider_type === 'openai_compatible') return '';
	const efforts = supportedEfforts(provider, entry.model_id);
	const current = String(entry.options.reasoning_effort ?? '');
	if (efforts.length === 0) return `Model ${entry.model_id} advertises no reasoning efforts.`;
	if (!efforts.includes(current)) return `Reasoning effort ${current || '(none)'} is not offered for model ${entry.model_id}.`;
	return '';
}

/** Default test tuple for one provider/stage: first catalog model with type defaults. */
export function blankTestForm(provider: AnalysisProvider, stage: StageID): TestForm {
	const model_id = modelChoices(provider)[0] ?? '';
	const options = defaultStageOptions(provider.type);
	if (provider.type !== 'openai_compatible') {
		options.reasoning_effort = firstAdvertisedEffort(provider, model_id) || 'low';
	}
	return { model_id, options };
}

/** Keep a test tuple valid when the model changes: reset an effort the new model does not advertise. */
export function retestModelChoice(provider: AnalysisProvider, form: TestForm, modelId: string): TestForm {
	const next: TestForm = { model_id: modelId, options: { ...form.options } };
	if (provider.type !== 'openai_compatible') {
		const efforts = supportedEfforts(provider, modelId);
		const current = String(next.options.reasoning_effort ?? '');
		if (!efforts.includes(current)) next.options.reasoning_effort = efforts[0] ?? 'low';
	}
	return next;
}

/** Wire request for one tested stage/model/options tuple. */
export function providerTestRequest(
	provider: Pick<AnalysisProvider, 'id' | 'type'>,
	stage: StageID,
	form: TestForm
): AnalysisProviderTestRequest {
	return {
		stage_id: stage,
		model_id: form.model_id,
		options: canonicalWireOptions({
			stage_id: stage,
			provider_id: provider.id,
			provider_type: provider.type,
			model_id: form.model_id,
			options: form.options
		})
	};
}

/** Bindings the server reports as no longer usable, with sanitized reasons. */
export function invalidBindings(profile: AnalysisProfile): AnalysisProfileBinding[] {
	return (profile.bindings ?? []).filter((binding) => binding.valid === false);
}

/** False when any stored binding is known-invalid; editing stays available. */
export function profileUsable(profile: AnalysisProfile): boolean {
	return invalidBindings(profile).length === 0;
}

/** Stable identity of one tested tuple: model plus canonical options. */
export function testTupleFingerprint(
	provider: Pick<AnalysisProvider, 'id' | 'type'>,
	stage: StageID,
	form: TestForm
): string {
	const request = providerTestRequest(provider, stage, form);
	return `${request.model_id ?? ''} ${JSON.stringify(request.options)}`;
}

/** Retained tuple results for one stage, in server order. */
export function stageConformance(provider: AnalysisProvider, stage: StageID): AnalysisConformanceSummary[] {
	return (provider.conformance ?? []).filter((summary) => summary.stage_id === stage);
}

/** Compact tuple label: model plus canonical options. */
export function conformanceTupleLabel(summary: AnalysisConformanceSummary): string {
	const options = summary.options ? ` ${JSON.stringify(summary.options)}` : '';
	return `${summary.model_id}${options}`;
}
