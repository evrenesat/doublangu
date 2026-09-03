import { describe, expect, it } from 'vitest';
import type { AnalysisProfile, AnalysisProvider } from '$lib/api/client';
import {
	STAGES,
	bindingEffortError,
	blankProfileDraft,
	blankTestForm,
	canonicalWireOptions,
	conformanceTupleLabel,
	defaultStageOptions,
	firstAdvertisedEffort,
	invalidBindings,
	stageOptionsError,
	profileDraftComplete,
	profileNameError,
	profileRequestFromDraft,
	profileUsable,
	providerTestRequest,
	retestModelChoice,
	stageConformance,
	stageLabel,
	supportedEfforts,
	testFormKey,
	testTupleFingerprint,
	usesNumericStageOptions
} from './analysisProfiles';

const codexProvider: AnalysisProvider = {
	id: 'codex-app-server',
	label: 'Codex',
	type: 'codex_app_server',
	enabled: true,
	stale: false,
	health: 'healthy',
	models: [
		{
			id: 'model-a',
			display_name: 'Model A',
			supported_reasoning_efforts: [{ value: 'minimal' }, { value: 'xhigh' }]
		},
		{ id: 'model-b', display_name: 'Model B' }
	]
};

const relayProvider: AnalysisProvider = {
	id: 'mac-relay',
	label: 'Mac relay',
	type: 'mac_relay',
	enabled: true,
	stale: false,
	health: 'healthy',
	models: [{ id: 'qwen-mlx', display_name: 'Qwen MLX' }]
};

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
	it('groups provider types by option schema like the server codecs', () => {
		expect(usesNumericStageOptions('openai_compatible')).toBe(true);
		expect(usesNumericStageOptions('mac_relay')).toBe(true);
		expect(usesNumericStageOptions('codex_app_server')).toBe(false);
		expect(usesNumericStageOptions('unknown')).toBe(false);
	});
	it('defaults options per provider type', () => {
		expect(defaultStageOptions('openai_compatible')).toEqual({ temperature_milli: 0, max_output_tokens: 16384 });
		expect(defaultStageOptions('mac_relay')).toEqual({ temperature_milli: 0, max_output_tokens: 16384 });
		expect(defaultStageOptions('codex_app_server')).toEqual({ reasoning_effort: 'low' });
	});
	it('preserves exact omlx values instead of clamping them', () => {
		const draft = {
			stage_id: 'translation' as const,
			provider_id: 'p',
			provider_type: 'openai_compatible',
			model_id: 'm',
			options: { temperature_milli: 9000, max_output_tokens: 5 }
		};
		const result = canonicalWireOptions(draft);
		expect(result.temperature_milli).toBe(9000);
		expect(result.max_output_tokens).toBe(5);
	});
	it('sends mac_relay numeric options as configured without injecting an effort', () => {
		const draft = {
			stage_id: 'translation' as const,
			provider_id: 'mac-relay',
			provider_type: 'mac_relay',
			model_id: 'qwen-mlx',
			options: { temperature_milli: 700, max_output_tokens: 8192 }
		};
		const result = canonicalWireOptions(draft);
		expect(result.temperature_milli).toBe(700);
		expect(result.max_output_tokens).toBe(8192);
		expect(result.reasoning_effort).toBeUndefined();
	});
	it('rejects non-integer and out-of-range options with explicit errors', () => {
		for (const numericType of ['openai_compatible', 'mac_relay']) {
			expect(stageOptionsError(numericType, { temperature_milli: 0, max_output_tokens: 16384 })).toBe('');
			expect(stageOptionsError(numericType, { temperature_milli: 9000, max_output_tokens: 16384 })).toContain('0 to 2000');
			expect(stageOptionsError(numericType, { temperature_milli: 0, max_output_tokens: 5 })).toContain('1024 to 65536');
			expect(stageOptionsError(numericType, { temperature_milli: 1.5, max_output_tokens: 16384 })).toContain('whole number');
			expect(stageOptionsError(numericType, { temperature_milli: null, max_output_tokens: 16384 })).toContain('whole number');
			expect(stageOptionsError(numericType, {})).toContain('whole number');
		}
		// A codex-style effort object is not a valid numeric-options payload.
		expect(stageOptionsError('mac_relay', { reasoning_effort: 'low' })).toContain('whole number');
		expect(stageOptionsError('codex_app_server', { reasoning_effort: 'low' })).toBe('');
		expect(stageOptionsError('codex_app_server', {})).toContain('required');
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
		expect(profileNameError('x'.repeat(80))).toBe('');
		expect(profileNameError('x'.repeat(81))).toContain('80');
		expect(profileNameError('bad\x01name')).toContain('control');
		expect(profileNameError('bad\x7fname')).toContain('control');
		expect(profileNameError('Mixed profile')).toBe('');
		// Astral characters count as one scalar each, matching the server.
		expect(profileNameError('😀'.repeat(80))).toBe('');
		expect(profileNameError('😀'.repeat(81))).toContain('80');
		draft.name = 'Mixed';
		draft.stages.linguistic_analysis.provider_id = 'codex-app-server';
		draft.stages.linguistic_analysis.model_id = 'model-a';
		expect(profileDraftComplete(draft)).toBe(false);
		draft.stages.translation.provider_id = 'omlx';
		draft.stages.translation.model_id = 'model-b';
		expect(profileDraftComplete(draft)).toBe(true);
	});
	it('sends only the four write fields per binding on a GET-to-PUT round trip', () => {
		// A stored profile read back from GET carries valid/validity_reason;
		// the editor rebuilds drafts with only write fields, and the wire
		// payload must never echo response-only validity state back, which
		// the strict server decoder would reject.
		const draft = blankProfileDraft();
		draft.name = 'Mixed';
		draft.stages.linguistic_analysis.provider_id = 'codex-app-server';
		draft.stages.linguistic_analysis.provider_type = 'codex_app_server';
		draft.stages.linguistic_analysis.model_id = 'model-a';
		draft.stages.linguistic_analysis.options = { reasoning_effort: 'low' };
		draft.stages.translation.provider_id = 'omlx';
		draft.stages.translation.provider_type = 'openai_compatible';
		draft.stages.translation.model_id = 'model-b';
		draft.stages.translation.options = { temperature_milli: 0, max_output_tokens: 32768 };
		const request = profileRequestFromDraft(draft);
		for (const binding of request.bindings) {
			expect(Object.keys(binding).sort()).toEqual(['model_id', 'options', 'provider_id', 'stage_id']);
		}
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

describe('conformance test tuples', () => {
	it('keys tuples by provider and stage', () => {
		expect(testFormKey('p', 'translation')).toBe('p::translation');
	});
	it('defaults a tuple to the first catalog model with type options', () => {
		const form = blankTestForm(codexProvider, 'linguistic_analysis');
		expect(form.model_id).toBe('model-a');
		expect(form.options.reasoning_effort).toBe('minimal');
	});
	it('defaults a mac_relay tuple to numeric options without an effort', () => {
		const form = blankTestForm(relayProvider, 'translation');
		expect(form.model_id).toBe('qwen-mlx');
		expect(form.options).toEqual({ temperature_milli: 0, max_output_tokens: 16384 });
	});
	it('keeps mac_relay options numeric when the tested model changes', () => {
		const form = blankTestForm(relayProvider, 'translation');
		const next = retestModelChoice(relayProvider, form, 'qwen-mlx');
		expect(next.options).toEqual({ temperature_milli: 0, max_output_tokens: 16384 });
	});
	it('builds a mac_relay tuple wire request with numeric options', () => {
		const request = providerTestRequest(relayProvider, 'translation', {
			model_id: 'qwen-mlx',
			options: { temperature_milli: 250, max_output_tokens: 2048 }
		});
		expect(request.stage_id).toBe('translation');
		expect(request.model_id).toBe('qwen-mlx');
		expect(request.options).toEqual({ temperature_milli: 250, max_output_tokens: 2048 });
	});
	it('resets an effort the newly chosen model does not advertise', () => {
		const form = blankTestForm(codexProvider, 'linguistic_analysis');
		const next = retestModelChoice(codexProvider, form, 'model-b');
		expect(next.model_id).toBe('model-b');
		expect(next.options.reasoning_effort).toBe('low');
		expect(form.model_id).toBe('model-a');
	});
	it('fingerprints a tuple by model and canonical options', () => {
		const form = { model_id: 'model-a', options: { reasoning_effort: 'xhigh' } };
		const same = testTupleFingerprint(codexProvider, 'translation', { model_id: 'model-a', options: { reasoning_effort: 'xhigh' } });
		expect(testTupleFingerprint(codexProvider, 'translation', form)).toBe(same);
		expect(testTupleFingerprint(codexProvider, 'translation', { model_id: 'model-b', options: { reasoning_effort: 'xhigh' } })).not.toBe(same);
		expect(testTupleFingerprint(codexProvider, 'translation', { model_id: 'model-a', options: { reasoning_effort: 'minimal' } })).not.toBe(same);
	});
	it('builds a tuple wire request without coercing the effort', () => {
		const request = providerTestRequest(codexProvider, 'translation', {
			model_id: 'model-a',
			options: { reasoning_effort: 'xhigh' }
		});
		expect(request.stage_id).toBe('translation');
		expect(request.model_id).toBe('model-a');
		expect(request.options.reasoning_effort).toBe('xhigh');
	});
	it('reports known-invalid bindings while keeping editing available', () => {
		const usable: AnalysisProfile = { id: 'p', name: 'Mixed', is_active: false, bindings: [] };
		expect(profileUsable(usable)).toBe(true);
		expect(invalidBindings(usable)).toEqual([]);
		const broken: AnalysisProfile = {
			...usable,
			bindings: [
				{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a', options: {}, valid: true },
				{ stage_id: 'translation', provider_id: 'mac-omlx', model_id: 'model-b', options: {}, valid: false, validity_reason: 'disabled provider' }
			]
		};
		expect(profileUsable(broken)).toBe(false);
		expect(invalidBindings(broken).map((binding) => binding.stage_id)).toEqual(['translation']);
		// Bindings from older servers without validity state stay selectable.
		const legacy: AnalysisProfile = { ...usable, bindings: [{ stage_id: 'translation', provider_id: 'x', model_id: 'm', options: {} }] };
		expect(profileUsable(legacy)).toBe(true);
	});
	it('resolves the first advertised effort and flags unoffered ones', () => {
		expect(firstAdvertisedEffort(codexProvider, 'model-a')).toBe('minimal');
		expect(firstAdvertisedEffort(codexProvider, 'model-b')).toBe('');
		expect(firstAdvertisedEffort(undefined, 'model-a')).toBe('');
		const entry = (model_id: string, effort: unknown, provider_type = 'codex_app_server') => ({
			stage_id: 'linguistic_analysis' as const,
			provider_id: 'codex-app-server',
			provider_type,
			model_id,
			options: { reasoning_effort: effort }
		});
		expect(bindingEffortError(codexProvider, entry('model-a', 'minimal'))).toBe('');
		expect(bindingEffortError(codexProvider, entry('model-a', 'low'))).toContain('not offered');
		expect(bindingEffortError(codexProvider, entry('model-b', 'low'))).toContain('no reasoning efforts');
		expect(bindingEffortError(codexProvider, entry('', ''))).toBe('');
		expect(
			bindingEffortError(codexProvider, entry('model-a', 'anything', 'openai_compatible'))
		).toBe('');
		// A mac_relay binding never carries an effort, so it never reports here.
		expect(bindingEffortError(relayProvider, entry('qwen-mlx', '', 'mac_relay'))).toBe('');
	});
	it('filters retained summaries per stage and labels tuples', () => {
		const options = { reasoning_effort: 'minimal' } as unknown as Record<string, never>;
		const provider: AnalysisProvider = {
			...codexProvider,
			conformance: [
				{ stage_id: 'linguistic_analysis', model_id: 'model-a', options, status: 'healthy', checked_at: '2026-01-02T00:00:00Z', duration_ms: 10 },
				{ stage_id: 'translation', model_id: 'model-b', status: 'unhealthy', checked_at: '2026-01-02T00:00:01Z', duration_ms: 0, error_code: 'v1.analysis_stage_failed' }
			]
		};
		expect(stageConformance(provider, 'translation').map((summary) => summary.model_id)).toEqual(['model-b']);
		expect(conformanceTupleLabel(provider.conformance![0]!)).toBe('model-a {"reasoning_effort":"minimal"}');
		expect(conformanceTupleLabel(provider.conformance![1]!)).toBe('model-b');
	});
});
