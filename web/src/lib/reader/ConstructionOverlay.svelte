<script lang="ts">
	import type { ArticleOccurrence } from '$lib/api/client';

	let { constructions, activeIDs }: { constructions: ArticleOccurrence[]; activeIDs: string[] } = $props();
</script>

<!-- The actual marks stay on the source spans so they never duplicate source
     text. This zero-size layer gives assistive/debug tooling one stable hook
     for the currently shared construction state. -->
<span
	class="construction-overlay"
	data-construction-count={constructions.length}
	data-active-constructions={activeIDs.join(' ')}
	aria-hidden="true"
></span>

<style>
	.construction-overlay {
		position: absolute;
		width: 0;
		height: 0;
		overflow: hidden;
		pointer-events: none;
	}
</style>
