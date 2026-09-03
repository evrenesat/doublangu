import type {
	AnalysisProfileBinding,
	AnalysisProfileInput,
	AnalysisProvider
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

export function profileNameError(name: string): string {
	const trimmed = name.trim();
	if (!trimmed) return 'Name is required.';
	if (trimmed.length > 120) return 'Name must be 120 characters or fewer.';
	return '';
}

/** Wire payload from a complete draft, with options canonicalized per type. */
export function profileRequestFromDraft(draft: ProfileDraft): AnalysisProfileInput {
	const bindings: AnalysisProfileBinding[] = STAGES.map((stage) => {
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
	if (entry.provider_type === 'openai_compatible') {
		options.temperature_milli = clampInteger(options.temperature_milli, 0, 2000, 0);
		options.max_output_tokens = clampInteger(options.max_output_tokens, 1024, 65536, 16384);
	} else {
		// The chosen effort is sent exactly as configured: the server
		// validates the model/effort pair against the provider catalog, so
		// the wire must never silently coerce an unsupported value into a
		// default that would change a saved profile.
		const effort = String(options.reasoning_effort ?? 'low').trim();
		options.reasoning_effort = effort || 'low';
	}
	return options as Record<string, never>;
}

function clampInteger(value: unknown, min: number, max: number, fallback: number): number {
	const parsed = typeof value === 'number' ? Math.round(value) : Number.parseInt(String(value), 10);
	if (!Number.isFinite(parsed)) return fallback;
	return Math.min(max, Math.max(min, parsed));
}

/** Model catalog choice for a provider; falls back to free-text model ids. */
export function modelChoices(provider: AnalysisProvider | undefined): string[] {
	const ids = (provider?.models ?? []).map((model) => model.id);
	return [...new Set(ids)];
}
