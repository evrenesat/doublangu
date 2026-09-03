<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import {
		clearNarration,
		DoublanguAPIError,
		DoublanguNetworkError,
		generateNarration,
		getNarration,
		getReaderSettings,
		listAnalysisProfiles,
		saveReaderSettings,
		reanalyzeArticle,
		updateSemanticLearningState,
		type AnalysisProfile,
		type Article,
		type ArticleBlock,
		type ArticleOccurrence,
		type ArticleSentence,
		type LearningStatus,
		type Narration,
		type SemanticLearningState
		} from '$lib/api/client';
	import { appPath } from '$lib/paths';
	import { compensateFocusReflow } from './focusController';
	import { HoverAudioController } from './audioController';
	import { applyReaderTheme, readReaderTheme, saveReaderTheme, type ReaderTheme } from './theme';
	import NarrationPlayer from './NarrationPlayer.svelte';
	import Paragraph from './Paragraph.svelte';
	import ReaderToolbar from './ReaderToolbar.svelte';
	import SemanticPopover from './SemanticPopover.svelte';
	import AnalysisProgressBar from './AnalysisProgressBar.svelte';
	import { waitStateProgress } from './progressText';

	type Props = {
		article: Article;
		onArticleChange: (article: Article) => void;
	};

	let props: Props = $props();
	const incomingArticle = $derived(props.article);
	let currentState = $state<Article | null>(null);
	const current = $derived(currentState ?? incomingArticle);
	let theme = $state<ReaderTheme>('midnight');
	const hoverPrefCacheKey = 'doublangu:reader:pronounce-on-hover';
	function readCachedHoverPreference(): boolean {
		try {
			const raw = localStorage.getItem(hoverPrefCacheKey);
			if (!raw) return true; // Existing installations default to enabled.
			const parsed = JSON.parse(raw) as { pronounce_on_hover?: boolean };
			return parsed.pronounce_on_hover !== false;
		} catch {
			return true;
		}
	}
	let hoverEnabled = $state(readCachedHoverPreference());
	let hoverPrefSaving = $state(false);
	let hoverActivationHint = $state(false);
	// Bumps on every save so an in-flight initial load can never overwrite a
	// newer successful toggle with its stale server snapshot.
	let settingsSaveVersion = 0;
	let selectedID = $state<string | null>(null);
	let anchor = $state<HTMLElement | null>(null);
	let pinned = $state(false);
	let activeSentenceID = $state<string | null>(null);
	let activeConstructionIDs = $state<string[]>([]);
	let feedback = $state('');
	let feedbackIsError = $state(false);
	let closeTimer: ReturnType<typeof setTimeout> | undefined;
	let focusCancel: (() => void) | undefined;
	let narration = $state<Narration | null>(null);
	let narrationLoadedFor = $state('');
	let narrationLoading = $state(false);
	let narrationError = $state('');
	let activeNarrationIndex = $state(0);
	let narrationPlaying = $state(false);
	let narrationSpeed = $state(1);
	let followFocus = $state(true);
	let reanalyzing = $state(false);
	let narrationRefreshKey = '';
	let pipelineProfiles = $state<AnalysisProfile[]>([]);
	let pipelineSelectedProfileID = $state('');
	let pipelineProfilesLoadedFor = '';
	let pipelineOptionsError = $state('');

	const hoverAudio = new HoverAudioController({ onBlocked: () => (hoverActivationHint = true) });

	function rememberHoverPreference(enabled: boolean): void {
		try {
			localStorage.setItem(hoverPrefCacheKey, JSON.stringify({ pronounce_on_hover: enabled }));
		} catch {
			// Local storage mirrors the last successful server value only.
		}
	}

	$effect(() => {
		currentState = incomingArticle;
	});

	function sortSentences(items: ArticleSentence[]): ArticleSentence[] {
		const blockOrder = new Map(current.blocks.map((block, index) => [block.id, index]));
		return items.slice().sort((left, right) => {
			const leftBlock = blockOrder.get(left.article_block_id) ?? Number.MAX_SAFE_INTEGER;
			const rightBlock = blockOrder.get(right.article_block_id) ?? Number.MAX_SAFE_INTEGER;
			return leftBlock - rightBlock || left.sentence_index - right.sentence_index || left.start_utf16 - right.start_utf16;
		});
	}

	const sentences = $derived.by((): ArticleSentence[] => {
		const topLevel = current.sentences ?? [];
		if (topLevel.length > 0) return sortSentences(topLevel);
		return sortSentences(current.blocks.flatMap((block) => block.sentences ?? []));
	});

	const occurrences = $derived.by((): ArticleOccurrence[] => {
		const topLevel = current.occurrences ?? [];
		if (topLevel.length > 0) return topLevel;
		return current.blocks.flatMap((block) => block.occurrences ?? []);
	});

	const blocks = $derived.by((): ArticleBlock[] =>
		current.blocks.map((block) => ({
			...block,
			sentences: block.sentences?.length
				? block.sentences
				: sentences.filter((sentence) => sentence.article_block_id === block.id),
			occurrences: block.occurrences?.length
				? block.occurrences
				: occurrences.filter((occurrence) => occurrence.article_block_id === block.id)
		}))
	);

	const selectedOccurrence = $derived(
		selectedID ? occurrences.find((occurrence) => occurrence.id === selectedID) ?? null : null
	);
	const selectedHearReference = $derived(selectedOccurrence ? hearReference(selectedOccurrence) : null);

	const narrationView = $derived.by((): Narration => {
		if (narration && narrationLoadedFor === current.id) return narration;
		const summary = current.narration;
		return {
			article_id: current.id,
			status: current.narration_status ?? summary?.status ?? 'not_requested',
			error_code: current.narration_error_code ?? summary?.error_code ?? '',
			sentence_count: summary?.sentence_count ?? sentences.length,
			ready_count: summary?.ready_count ?? 0,
			duration_ms: summary?.duration_ms ?? 0,
			size_bytes: summary?.size_bytes ?? 0,
			reclaimable_bytes: summary?.reclaimable_bytes ?? 0,
			clips: []
		};
	});

	const analysisLabel = $derived.by(() => {
		switch (current.analysis_status) {
			case 'queued':
			case 'processing': return 'Preparing English subtitles…';
			case 'ready': return 'Ready';
			case 'failed': return 'Needs retry';
		default: return 'Waiting to start';
		}
	});

	const analysisSelectionLabel = $derived(
		current.analysis_model && current.analysis_effort
			? `${current.analysis_model} / ${current.analysis_effort}`
			: 'selected model'
	);
	const pipelineConfigured = $derived(Boolean(current.analysis_pipeline?.profile_id));

	$effect(() => {
		const id = current.id;
		if (!pipelineConfigured || pipelineProfilesLoadedFor === id) return;
		pipelineProfilesLoadedFor = id;
		pipelineOptionsError = '';
		void (async () => {
			try {
				const result = await listAnalysisProfiles();
				pipelineProfiles = result.profiles;
				const stored = current.analysis_pipeline?.profile_id ?? '';
				pipelineSelectedProfileID =
					result.profiles.some((profile) => profile.id === stored) ? stored :
					(result.profiles.find((profile) => profile.is_active)?.id ?? '');
			} catch (cause) {
				pipelineOptionsError = errorMessage(cause, 'Could not load analysis profiles.');
			}
		})();
	});

	const speechLabel = $derived.by(() => {
		switch (current.narration_status) {
			case 'queued':
			case 'generating': return 'Waiting for speech worker…';
			case 'partial': return 'Partly ready';
			case 'ready': return 'Ready';
			case 'failed': return 'Needs retry';
		case 'purged': return 'Cleared';
		default: return 'Not requested';
		}
	});

	const waitShape = $derived.by(() =>
		waitStateProgress({
			analysis_status: current.analysis_status,
			narration_status: current.narration_status,
			total: current.analysis_progress?.total_paragraphs,
			completed: current.analysis_progress?.completed_paragraphs,
			current_block_index: current.analysis_progress?.current_block_index,
			failed_block_index: current.analysis_progress?.failed_block_index,
			ready_sentences: current.narration?.ready_count,
			total_sentences: current.narration?.sentence_count
		})
	);

	$effect(() => {
		if (!narration || narrationLoadedFor !== current.id || narrationLoading) return;
		const status = current.narration_status ?? current.narration?.status ?? 'not_requested';
		const readyCount = current.narration?.ready_count ?? 0;
		const summaryKey = `${current.id}:${status}:${readyCount}`;
		const manifestMatches = narration.status === status && narration.ready_count === readyCount;
		if (manifestMatches || narrationRefreshKey === summaryKey) return;
		narrationRefreshKey = summaryKey;
		void loadNarration(true);
	});

	onMount(() => {
		theme = readReaderTheme();
		applyReaderTheme(theme);
		hoverAudio.setEnabled(hoverEnabled);
		const handleKeydown = (event: KeyboardEvent) => {
			if (event.key !== 'Escape') return;
			if (selectedID) {
				closePinned();
				return;
			}
			clearFocus();
		};
		document.addEventListener('keydown', handleKeydown);
		// While hover pronunciation is enabled, register one passive unlock
		// attempt per user activation so browsers that block script-initiated
		// audio still unlock on the first pointerdown/keydown.
		const unlockOnActivation = (event: Event) => {
			if (!hoverEnabled || hoverPrefSaving) return;
			if (event.type === 'keydown' && (event as KeyboardEvent).key !== 'Enter' && (event as KeyboardEvent).key !== ' ') return;
			hoverActivationHint = false;
			void hoverAudio.unlock();
		};
		window.addEventListener('pointerdown', unlockOnActivation, { passive: true });
		window.addEventListener('keydown', unlockOnActivation, { passive: true });
		void refreshServerPreference();
		return () => {
			document.removeEventListener('keydown', handleKeydown);
			window.removeEventListener('pointerdown', unlockOnActivation);
			window.removeEventListener('keydown', unlockOnActivation);
		};
	});

	async function refreshServerPreference(): Promise<void> {
		const version = settingsSaveVersion;
		try {
			const server = await getReaderSettings();
			if (version !== settingsSaveVersion) return; // a save superseded this load
			hoverEnabled = server.pronounce_on_hover;
			hoverAudio.setEnabled(server.pronounce_on_hover);
			rememberHoverPreference(server.pronounce_on_hover);
		} catch {
			// The server remains authoritative on the next successful load;
			// the cached mirror avoids first-render flicker meanwhile.
		}
	}

	onDestroy(() => {
		if (closeTimer) clearTimeout(closeTimer);
		focusCancel?.();
		hoverAudio.destroy();
	});

	function emit(next: Article): void {
		currentState = next;
		props.onArticleChange(next);
	}

	function setTheme(next: ReaderTheme): void {
		theme = next;
		applyReaderTheme(next);
		saveReaderTheme(next);
	}

	async function toggleHoverAudio(): Promise<void> {
		const next = !hoverEnabled;
		const previous = hoverEnabled;
		// Optimistic local change with an inline rollback when the server
		// rejects the save. The version marker invalidates any settings load
		// that was already in flight.
		settingsSaveVersion += 1;
		hoverEnabled = next;
		hoverAudio.setEnabled(next);
		rememberHoverPreference(next);
		hoverPrefSaving = true;
		feedback = '';
		feedbackIsError = false;
		try {
			const saved = await saveReaderSettings({ pronounce_on_hover: next });
			hoverEnabled = saved.pronounce_on_hover;
			hoverAudio.setEnabled(saved.pronounce_on_hover);
			rememberHoverPreference(saved.pronounce_on_hover);
		} catch (cause) {
			hoverEnabled = previous;
			hoverAudio.setEnabled(previous);
			rememberHoverPreference(previous);
			feedbackIsError = true;
			feedback = errorMessage(cause, 'Could not save the pronounce-on-hover preference.');
		} finally {
			hoverPrefSaving = false;
		}
	}

	function clearCloseTimer(): void {
		if (closeTimer) clearTimeout(closeTimer);
		closeTimer = undefined;
	}

	function openPreview(occurrence: ArticleOccurrence, target: HTMLElement): void {
		if (pinned) return;
		clearCloseTimer();
		selectedID = occurrence.id;
		anchor = target;
		feedback = '';
		feedbackIsError = false;
	}

	function openPinned(occurrence: ArticleOccurrence, target: HTMLElement, pin: boolean): void {
		clearCloseTimer();
		selectedID = occurrence.id;
		anchor = target;
		if (pin) pinned = true;
		if (occurrence.role === 'discontinuous_construction') activeConstructionIDs = [occurrence.id];
		feedback = '';
		feedbackIsError = false;
	}

	function scheduleClose(): void {
		if (pinned) return;
		clearCloseTimer();
		closeTimer = setTimeout(close, 120);
	}

	function keepPopoverOpen(): void {
		clearCloseTimer();
	}

	function close(): void {
		clearCloseTimer();
		if (pinned) return;
		selectedID = null;
		anchor = null;
		activeConstructionIDs = [];
	}

	function closePinned(): void {
		pinned = false;
		selectedID = null;
		anchor = null;
		activeConstructionIDs = [];
		feedback = '';
		clearCloseTimer();
	}

	function handleConstructionHover(ids: string[]): void {
		if (!pinned) activeConstructionIDs = ids;
	}

	function handleHoverAudio(occurrence: ArticleOccurrence, pointerType: string): void {
		if (pointerType === 'touch') return;
		hoverAudio.enter(occurrence.pronunciation, occurrence.pronunciation?.render_id ?? occurrence.id);
	}

	function handleLeaveAudio(key: string): void {
		hoverAudio.leave(key);
	}

	function sentenceForOccurrence(occurrence: ArticleOccurrence): ArticleSentence | null {
		if (occurrence.article_sentence_id) {
			return sentences.find((sentence) => sentence.id === occurrence.article_sentence_id) ?? null;
		}
		const firstSpan = occurrence.spans[0];
		if (!firstSpan) return null;
		return sentences.find((sentence) => firstSpan.start_utf16 >= sentence.start_utf16 && firstSpan.end_utf16 <= sentence.end_utf16) ?? null;
	}

	function hearReference(occurrence: ArticleOccurrence): ArticleOccurrence['pronunciation'] {
		if (occurrence.role === 'discontinuous_construction') return sentenceForOccurrence(occurrence)?.audio ?? null;
		return occurrence.pronunciation;
	}

	async function hearSelected(): Promise<void> {
		const occurrence = selectedOccurrence;
		if (!occurrence) return;
		const reference = hearReference(occurrence);
		if (!reference?.ready) {
			feedback = occurrence.role === 'discontinuous_construction' ? 'Sentence audio preparing…' : 'Audio preparing…';
			feedbackIsError = false;
			return;
		}
		feedback = '';
		await hoverAudio.playNow(reference, `explicit:${reference.render_id}`);
	}

	function withSemanticLearning(article: Article, senseID: string, state: SemanticLearningState): Article {
		const update = (occurrence: ArticleOccurrence): ArticleOccurrence => {
			if (occurrence.semantic_sense_id !== senseID) return occurrence;
			return {
				...occurrence,
				learning_state: state,
				show_shadow:
					state.status !== 'learned' && occurrence.subtitle_suppression_reason === 'none'
			};
		};
		const next: Article = {
			...article,
			blocks: article.blocks.map((block) => ({
				...block,
				occurrences: block.occurrences?.map(update)
			}))
		};
		if (article.occurrences) next.occurrences = article.occurrences.map(update);
		return next;
	}

	async function saveLearningStatus(status: LearningStatus): Promise<void> {
		const occurrence = selectedOccurrence;
		if (!occurrence?.semantic_sense_id) return;
		const previous = current;
		const optimistic: SemanticLearningState = {
			semantic_sense_id: occurrence.semantic_sense_id,
			status,
			updated_at: new Date().toISOString()
		};
		emit(withSemanticLearning(current, occurrence.semantic_sense_id, optimistic));
		try {
			const saved = await updateSemanticLearningState({
				semantic_sense_id: occurrence.semantic_sense_id,
				article_occurrence_id: occurrence.id,
				status
			});
			emit(withSemanticLearning(current, occurrence.semantic_sense_id, saved));
			feedback = status === 'learned' ? 'Marked learned. Subtitle hidden.' : 'Marked unlearned. Subtitle restored.';
			feedbackIsError = false;
		} catch (cause) {
			emit(previous);
			feedbackIsError = true;
			feedback = errorMessage(cause, 'Could not save learning state.');
			throw cause;
		}
	}

	function focusSentence(sentenceID: string, element: HTMLElement): void {
		focusCancel?.();
		focusCancel = compensateFocusReflow(element, () => {
			activeSentenceID = sentenceID;
		});
		if (followFocus) setNarrationIndex(sentenceID);
	}

	function clearFocus(): void {
		const previousID = activeSentenceID;
		if (!previousID) return;
		const element = findSentenceElement(previousID);
		if (element) {
			focusCancel?.();
			focusCancel = compensateFocusReflow(element, () => {
				activeSentenceID = null;
			});
		} else {
			activeSentenceID = null;
		}
	}

	function findSentenceElement(sentenceID: string): HTMLElement | null {
		return Array.from(document.querySelectorAll<HTMLElement>('[data-sentence-id]')).find((element) => element.dataset.sentenceId === sentenceID) ?? null;
	}

	function setNarrationIndex(sentenceID: string): void {
		const index = narration?.clips.findIndex((clip) => clip.sentence_id === sentenceID) ?? -1;
		if (index >= 0) activeNarrationIndex = index;
	}

	function focusNarrationSentence(): void {
		if (!followFocus) return;
		const sentenceID = narration?.clips[activeNarrationIndex]?.sentence_id;
		if (!sentenceID) return;
		const element = findSentenceElement(sentenceID);
		if (!element) return;
		focusSentence(sentenceID, element);
	}

	async function loadNarration(force = false): Promise<Narration | null> {
		if (!force && narration && narrationLoadedFor === current.id) return narration;
		narrationLoading = true;
		narrationError = '';
		try {
			const loaded = await getNarration(current.id);
			narration = loaded;
			narrationLoadedFor = current.id;
			activeNarrationIndex = Math.min(activeNarrationIndex, Math.max(0, loaded.clips.length - 1));
			prefetchAudio(loaded);
			return loaded;
		} catch (cause) {
			narrationError = errorMessage(cause, 'Could not load narration.');
			return null;
		} finally {
			narrationLoading = false;
		}
	}

	async function playNarration(): Promise<void> {
		const loaded = await loadNarration();
		if (!loaded) return;
		const readyIndex = loaded.clips.findIndex((clip, index) => index >= activeNarrationIndex && clip.audio?.ready);
		if (readyIndex < 0) {
			feedback = loaded.status === 'failed' ? 'Narration generation failed. Try regenerating it.' : 'Narration is still preparing.';
			feedbackIsError = loaded.status === 'failed';
			return;
		}
		activeNarrationIndex = readyIndex;
		narrationPlaying = true;
		focusNarrationSentence();
	}

	function pauseNarration(): void {
		narrationPlaying = false;
	}

	function previousNarration(): void {
		if (!narration || activeNarrationIndex <= 0) return;
		activeNarrationIndex -= 1;
		focusNarrationSentence();
	}

	function nextNarration(): void {
		if (!narration || activeNarrationIndex >= narration.clips.length - 1) return;
		activeNarrationIndex += 1;
		focusNarrationSentence();
	}

	function narrationEnded(): void {
		if (narration && activeNarrationIndex < narration.clips.length - 1) {
			activeNarrationIndex += 1;
			focusNarrationSentence();
			return;
		}
		narrationPlaying = false;
	}

	function prefetchAudio(loaded: Narration = narrationView): void {
		const visible = blocks[0]?.occurrences?.map((occurrence) => occurrence.pronunciation) ?? [];
		const next = loaded.clips[activeNarrationIndex + 1]?.audio;
		hoverAudio.prefetch([...visible, next], 4);
	}

	function updateNarrationSummary(next: Article, result: { status: string; reclaimed_bytes?: number; retained_bytes?: number; sentence_count?: number }): Article {
		const summary = next.narration;
		const purged = result.status === 'purged';
		return {
			...next,
			narration_status: result.status as Article['narration_status'],
			narration_error_code: '',
			narration: summary
				? {
					...summary,
					status: result.status as NonNullable<Article['narration']>['status'],
					sentence_count: result.sentence_count ?? summary.sentence_count,
					ready_count: purged ? 0 : summary.ready_count,
					duration_ms: purged ? 0 : summary.duration_ms,
					size_bytes: purged ? 0 : result.retained_bytes ?? summary.size_bytes,
					reclaimable_bytes: purged ? 0 : summary.reclaimable_bytes
				}
				: next.narration
		};
	}

	async function regenerateNarration(): Promise<void> {
		narrationLoading = true;
		narrationError = '';
		try {
			const next = await generateNarration(current.id);
			narration = null;
			narrationLoadedFor = '';
			narrationRefreshKey = '';
			narrationPlaying = false;
			emit(next);
		} catch (cause) {
			narrationError = errorMessage(cause, 'Could not queue narration.');
		} finally {
			narrationLoading = false;
		}
	}

	async function clearArticleNarration(): Promise<void> {
		try {
			const result = await clearNarration(current.id);
			narration = null;
			narrationLoadedFor = '';
			narrationRefreshKey = '';
			narrationPlaying = false;
			emit(updateNarrationSummary(current, result));
		} catch (cause) {
			narrationError = errorMessage(cause, 'Could not clear narration.');
			throw cause;
		}
	}

	async function retryAnalysis(fresh = false): Promise<void> {
		reanalyzing = true;
		feedback = '';
		feedbackIsError = false;
		try {
			emit(await reanalyzeArticle(current.id, fresh, fresh ? pipelineSelectedProfileID : ''));
		} catch (cause) {
			feedbackIsError = true;
			feedback = errorMessage(cause, 'Could not queue analysis.');
		} finally {
			reanalyzing = false;
		}
	}

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}
</script>

<section class="reader-shell" aria-label="Audible article reader">
	<div class="reader-status-row">
		<div class="status-item">
				<span class="status-label">English subtitles</span>
				<strong class:status-ready={current.analysis_status === 'ready'} class:status-error={current.analysis_status === 'failed'}>{analysisLabel}</strong>
				{#if pipelineConfigured}
					<a class="analysis-provenance" href={appPath('/settings')}>Profile: {current.analysis_pipeline!.profile_name || current.analysis_pipeline!.profile_id}</a>
				{:else if current.analysis_model}
					<span class="analysis-provenance">{current.analysis_model} · {current.analysis_effort}</span>
				{:else}
					<a class="analysis-provenance" href={appPath('/settings')}>Choose a model in Settings</a>
				{/if}
				{#if pipelineConfigured}
					{#if current.analysis_status === 'failed'}
						<button type="button" class="status-action" disabled={reanalyzing} onclick={() => void retryAnalysis()}>{reanalyzing ? 'Retrying…' : 'Retry with saved profile'}</button>
						<a class="status-action secondary-action" href={appPath('/settings')}>Change in Settings</a>
					{/if}
					<span class="fresh-run">
						<select
							aria-label="Profile for a fresh analysis run"
							bind:value={pipelineSelectedProfileID}
							disabled={pipelineProfiles.length === 0}
						>
							<option value="">Use the active profile</option>
							{#each pipelineProfiles as profile (profile.id)}
								<option value={profile.id}>{profile.name}{profile.is_active ? ' (active)' : ''}</option>
							{/each}
						</select>
						<button type="button" class="status-action secondary-action" disabled={reanalyzing} onclick={() => void retryAnalysis(true)}>
							{reanalyzing ? 'Running…' : 'Run fresh analysis'}
						</button>
					</span>
					{#if pipelineOptionsError}<span class="reader-error" role="alert">{pipelineOptionsError}</span>{/if}
				{:else if current.analysis_status === 'failed'}
					<button type="button" class="status-action" disabled={reanalyzing} onclick={() => void retryAnalysis()}>{reanalyzing ? 'Retrying…' : `Retry with ${analysisSelectionLabel}`}</button>
					<a class="status-action secondary-action" href={appPath('/settings')}>Change in Settings</a>
					{#if current.analysis_revision}
						<button type="button" class="status-action secondary-action" disabled={reanalyzing} onclick={() => void retryAnalysis(true)}>Run fresh analysis</button>
					{/if}
				{:else if current.analysis_status === 'ready'}
					<button type="button" class="status-action secondary-action" disabled={reanalyzing} onclick={() => void retryAnalysis(true)}>{reanalyzing ? 'Running…' : 'Run fresh analysis'}</button>
				{/if}
		</div>
		<div class="status-item">
			<span class="status-label">Speech</span>
			<strong class:status-ready={current.narration_status === 'ready'} class:status-error={current.narration_status === 'failed'}>{speechLabel}</strong>
		</div>
	</div>

	<ReaderToolbar
		hoverEnabled={hoverEnabled}
		hoverSaving={hoverPrefSaving}
		theme={theme}
		onToggleHover={() => void toggleHoverAudio()}
		onTheme={setTheme}
	/>
	{#if hoverActivationHint}
		<p class="reader-error hover-hint" role="status">Click once to enable sound</p>
	{/if}

	<div class="reader-body" class:has-focus={activeSentenceID !== null}>
		<AnalysisProgressBar shape={waitShape} />
		{#each blocks as block (block.id)}
			<Paragraph
				{block}
				activeSentenceID={activeSentenceID}
				activeConstructionIDs={activeConstructionIDs}
				onOpen={openPinned}
				onPreview={openPreview}
				onHoverEnd={scheduleClose}
				onHoverAudio={handleHoverAudio}
				onLeaveAudio={handleLeaveAudio}
				onConstructionHover={handleConstructionHover}
				onFocusSentence={focusSentence}
			/>
		{/each}
	</div>

	{#if selectedOccurrence && anchor}
		<SemanticPopover
			occurrence={selectedOccurrence}
			{anchor}
			feedback={feedback}
			feedbackIsError={feedbackIsError}
			onEnter={keepPopoverOpen}
			onLeave={scheduleClose}
			onClose={closePinned}
			onLearningStatus={saveLearningStatus}
			onHear={() => void hearSelected()}
			hearReady={Boolean(selectedHearReference?.ready)}
			hearPending={Boolean(selectedHearReference && !selectedHearReference.ready)}
		/>
	{/if}

	<NarrationPlayer
		narration={narrationView}
		activeIndex={activeNarrationIndex}
		playing={narrationPlaying}
		speed={narrationSpeed}
		followFocus={followFocus}
		loading={narrationLoading}
		onPlay={() => void playNarration()}
		onPause={pauseNarration}
		onPrevious={previousNarration}
		onNext={nextNarration}
		onEnded={narrationEnded}
		onSpeed={(speed) => (narrationSpeed = speed)}
		onFollowFocus={() => (followFocus = !followFocus)}
		onRegenerate={() => void regenerateNarration()}
		onClear={clearArticleNarration}
	/>
	{#if narrationError}<p class="reader-error" role="alert">{narrationError}</p>{/if}
	{#if feedback && feedbackIsError && !selectedOccurrence}<p class="reader-error" role="alert">{feedback}</p>{/if}
</section>

<style>
	.reader-shell {
		--reader-bg: var(--reader-page-bg);
		--reader-surface: var(--reader-page-surface);
		--reader-surface-raised: var(--reader-page-raised);
		--reader-border: var(--reader-page-border);
		--reader-text: var(--reader-page-text);
		--reader-muted: var(--reader-page-muted);
		--reader-accent: var(--reader-page-accent);
		--reader-construction: var(--reader-page-construction);
		--reader-subtitle: var(--reader-page-subtitle);
		--reader-danger: #ffabbc;
		max-width: 54rem;
		margin: 0 auto;
		color: var(--reader-text);
	}

	.reader-status-row {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.6rem;
		margin-bottom: 0.7rem;
	}

	.status-item {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.45rem;
		min-width: 0;
		padding: 0.6rem 0.7rem;
		border: 1px solid var(--reader-border);
		border-radius: 0.6rem;
		background: var(--reader-surface);
		font-size: 0.82rem;
	}

	.status-label { color: var(--reader-muted); }
	.status-item strong { font-weight: 650; }
	.analysis-provenance { color: var(--reader-muted); font-size: 0.75rem; overflow-wrap: anywhere; }
	.status-ready { color: #a9e6bd; }
	.status-error { color: var(--reader-danger); }
	.status-action {
		margin-left: auto;
		padding: 0.25rem 0.45rem;
		border: 1px solid currentColor;
		border-radius: 999px;
		background: transparent;
		color: inherit;
		font-size: 0.78rem;
		cursor: pointer;
	}
	.status-action:disabled { cursor: wait; opacity: 0.6; }
	.secondary-action { color: var(--reader-muted); }

	.reader-body {
		position: relative;
		padding: clamp(1.1rem, 3vw, 2.65rem) clamp(1rem, 4vw, 3rem);
		border: 1px solid var(--reader-border);
		border-radius: 0.8rem;
		background: var(--reader-bg);
		box-shadow: 0 18px 50px rgb(0 0 0 / 18%);
	}

	.reader-body.has-focus :global(.reader-paragraph) { transition: opacity 120ms ease; }
	.reader-body.has-focus :global(.reader-paragraph:not(:has(.reader-sentence.focused))) { opacity: 0.74; }

	.reader-error { margin: 0.65rem 0 0; color: var(--reader-danger); font-size: 0.85rem; }

	@media (max-width: 600px) {
		.reader-status-row { grid-template-columns: 1fr; }
	}

	@media (prefers-reduced-motion: reduce) {
		.reader-body.has-focus :global(.reader-paragraph) { transition: none; }
	}

	.fresh-run { display: inline-flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
	.fresh-run select { padding: 0.3rem 0.45rem; font: inherit; max-width: 16rem; }
</style>
