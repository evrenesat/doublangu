<script lang="ts">
	import { tick } from 'svelte';
	import type { ArticleOccurrence } from '$lib/api/client';
	let { constructions, activeIDs, root = null }: {
		constructions: ArticleOccurrence[]; activeIDs: string[]; root?: HTMLElement | null;
	} = $props();
	type Mark = { id: string; d: string; split: boolean; label?: { x: number; y: number; text: string } };
	let marks = $state<Mark[]>([]);
	let width = $state(1);
	let height = $state(1);
	$effect(() => {
		const container = root;
		const groups = constructions;
		if (!container) return;
		let disposed = false;
		function measure() {
			if (disposed || !container) return;
			width = container.clientWidth;
			height = container.clientHeight;
			const tokens = Array.from(container.querySelectorAll<HTMLElement>('[data-occurrence-id]'));
			const next: Mark[] = [];
			groups.forEach((group, lane) => {
				const parts = tokens.filter((token) => token.dataset.constructionIds?.split(' ').includes(group.id));
				const clusters: HTMLElement[][] = [];
				for (const part of parts) {
					const last = clusters.at(-1)?.at(-1);
					if (last && tokens.indexOf(part) === tokens.indexOf(last) + 1 && Math.abs(last.offsetTop - part.offsetTop) < 4) clusters.at(-1)!.push(part);
					else clusters.push([part]);
				}
				const boxes = clusters.map((cluster) => ({
					left: cluster[0]!.offsetLeft, right: cluster.at(-1)!.offsetLeft + cluster.at(-1)!.offsetWidth,
					top: cluster[0]!.offsetTop,
					bottom: Math.max(...cluster.map((part) => part.offsetTop + part.offsetHeight)) + 3 + lane * 6
				}));
				for (const box of boxes) {
					if (box.right - box.left > 1) next.push({ id: group.id, split: false,
						d: `M ${box.left} ${box.bottom} Q ${box.left} ${box.bottom + 5}, ${box.left + 5} ${box.bottom + 5} L ${box.right - 5} ${box.bottom + 5} Q ${box.right} ${box.bottom + 5}, ${box.right} ${box.bottom}` });
				}
				for (let index = 1; index < boxes.length; index++) {
					const a = boxes[index - 1]!;
					const b = boxes[index]!;
					const y1 = a.bottom + 5;
					const y2 = b.bottom + 5;
					const x1 = a.right - 3;
					const x2 = b.left + 3;
					if (Math.abs(a.top - b.top) < 4) {
						next.push({ id: group.id, split: true, d: `M ${x1} ${y1} C ${x1 + 14} ${y1 + 12}, ${x2 - 14} ${y2 + 12}, ${x2} ${y2}` });
					} else {
						// Continue at the margins; never cross intervening source lines.
						next.push({ id: group.id, split: true, d: `M ${x1} ${y1} Q ${x1 + 12} ${y1 + 10}, ${width - 12} ${y1 + 10}`,
							label: { x: width - 5, y: y1 + 13, text: String(lane + 1) } });
						next.push({ id: group.id, split: true, d: `M 12 ${y2 + 10} Q ${x2 - 12} ${y2 + 10}, ${x2} ${y2}`,
							label: { x: 5, y: y2 + 13, text: String(lane + 1) } });
					}
				}
			});
			marks = next;
		}
		const observer = new ResizeObserver(measure);
		observer.observe(container);
		void tick().then(() => { if (!disposed) { container.querySelectorAll('[data-occurrence-id]').forEach((item) => observer.observe(item)); measure(); } });
		void document.fonts.ready.then(measure);
		return () => { disposed = true; observer.disconnect(); };
	});
</script>

<svg class="construction-overlay" viewBox={`0 0 ${width} ${height}`} aria-hidden="true"
	data-construction-count={constructions.length} data-active-constructions={activeIDs.join(' ')}>
	{#each marks as mark, index (index)}
		<g class:active={activeIDs.includes(mark.id)} data-connector-for={mark.id}>
			<path d={mark.d} stroke-dasharray={mark.split ? '3 5' : undefined} />
			{#if mark.label}<text x={mark.label.x} y={mark.label.y}>{mark.label.text}</text>{/if}
		</g>
	{/each}
</svg>

<style>
	.construction-overlay { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; overflow: visible; color: var(--reader-construction); }
	path { fill: none; stroke: currentColor; stroke-width: 1.5; }
	.active path { stroke-width: 2.4; }
	text { fill: currentColor; text-anchor: middle; font: 10px ui-sans-serif, system-ui, sans-serif; }
</style>
