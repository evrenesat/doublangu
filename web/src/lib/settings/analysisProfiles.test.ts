import { describe, expect, it } from 'vitest';
import type { AnalysisProvider } from '$lib/api/client';
import {
	STAGES,
	blankProfileDraft,
	canonicalWireOptions,
	defaultStageOptions,
	profileDraftComplete,
	profileNameError,
	profileRequestFromDraft,
	stageLabel,
	supportedEfforts
} from './analysisProfiles';

describe('stage metadata', () => {
	it('keeps both stages in registered order', () => {
		expect(STAGES).toEqual(['linguistic_analysis', 'translation']);
	});
	it('labels stages and falls back for unknown ids', () => {
		expect(stageLabel('linguistic_analysis')).toBe('Linguistic analysis');
		expect(stageLabel('translation')).toBe('Translation');
		expect(stageLabel('other')).toBe('other');
	});
});

describe('options defaults and canonicalization', () => {
	it('defaults options per provider type', () => {
		expect(defaultStageOptions('openai_compatible')).toEqual({ temperature_milli: 0, max_output_tokens: 16384 });
		expect(defaultStageOptions('codex_app_server')).toEqual({ reasoning_effort: 'low' });
	});
	it('clamps omlx option bounds', () => {
		const draft = {
			stage_id: 'translation' as const,
			provider_id: 'p',
			provider_type: 'openai_compatible',
			model_id: 'm',
			options: { temperature_milli: 9000, max_output_tokens: 5 }
		};
		const result = canonicalWireOptions(draft);
		expect(result.temperature_milli).toBe(2000);
		expect(result.max_output_tokens).toBe(1024);
	});
	it('preserves the chosen codex effort without coercion', () => {
		const draft = {
			stage_id: 'linguistic_analysis' as const,
			provider_id: 'p',
			provider_type: 'codex_app_server',
			model_id: 'm',
			options: { reasoning_effort: 'minimal' }
		};
		expect(canonicalWireOptions(draft).reasoning_effort).toBe('minimal');
	});
	it('defaults a missing codex effort to low', () => {
		const draft = {
			stage_id: 'linguistic_analysis' as const,
			provider_id: 'p',
			provider_type: 'codex_app_server',
			model_id: 'm',
			options: {}
		};
		expect(canonicalWireOptions(draft).reasoning_effort).toBe('low');
	});
	it('lists only the reasoning efforts the selected model advertises', () => {
		const provider: AnalysisProvider = {
			id: 'codex-app-server',
			label: 'Codex',
			type: 'codex_app_server',
			enabled: true,
			stale: false,
			health: 'healthy',
			models: [
				{
					id: 'model-x',
					display_name: 'Model X',
					supported_reasoning_efforts: [{ value: 'minimal' }, { value: 'xhigh' }]
				}
			]
		};
		expect(supportedEfforts(provider, 'model-x')).toEqual(['minimal', 'xhigh']);
		expect(supportedEfforts(provider, 'model-y')).toEqual([]);
		expect(supportedEfforts(undefined, 'model-x')).toEqual([]);
	});
});

describe('profile draft completeness and payload', () => {
	it('requires name and both resolved stages', () => {
		const draft = blankProfileDraft();
		expect(profileDraftComplete(draft)).toBe(false);
		expect(profileNameError('')).toBe('Name is required.');
		expect(profileNameError('x'.repeat(121))).toContain('120');
		expect(profileNameError('Mixed profile')).toBe('');
		draft.name = 'Mixed';
		draft.stages.linguistic_analysis.provider_id = 'codex-app-server';
		draft.stages.linguistic_analysis.model_id = 'model-a';
		expect(profileDraftComplete(draft)).toBe(false);
		draft.stages.translation.provider_id = 'omlx';
		draft.stages.translation.model_id = 'model-b';
		expect(profileDraftComplete(draft)).toBe(true);
	});
	it('builds bindings in registered order for the wire', () => {
		const draft = blankProfileDraft();
		draft.name = 'Mixed';
		draft.stages.linguistic_analysis.provider_id = 'codex-app-server';
		draft.stages.linguistic_analysis.provider_type = 'codex_app_server';
		draft.stages.linguistic_analysis.model_id = 'model-a';
		draft.stages.translation.provider_id = 'omlx';
		draft.stages.translation.provider_type = 'openai_compatible';
		draft.stages.translation.model_id = 'model-b';
		draft.stages.translation.options = { temperature_milli: 0, max_output_tokens: 32768 };
		const request = profileRequestFromDraft(draft);
		expect(request.name).toBe('Mixed');
		expect(request.bindings.map((binding) => binding.stage_id)).toEqual(STAGES);
		expect(request.bindings[0]!.provider_id).toBe('codex-app-server');
		expect(request.bindings[1]!.options.max_output_tokens).toBe(32768);
	});
});
