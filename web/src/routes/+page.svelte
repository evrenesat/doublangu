<script lang="ts">
	import { onMount } from 'svelte';

	let healthStatus = $state<'loading' | 'ok' | 'error'>('loading');
	let version = $state('');

	onMount(() => {
		fetch('/health/live')
			.then((r) => {
				if (r.ok) return r.json();
				throw new Error('unhealthy');
			})
			.then((data) => {
				healthStatus = 'ok';
				version = data.version ?? '';
			})
			.catch(() => {
				healthStatus = 'error';
			});
	});
</script>

<svelte:head>
	<title>Doublangu</title>
</svelte:head>

<h1>Doublangu</h1>

<div class="health">
	<p>
		<span class="dot" class:green={healthStatus === 'ok'} class:red={healthStatus === 'error'} class:gray={healthStatus === 'loading'}></span>
		Server status:
		{#if healthStatus === 'loading'}
			<em>checking…</em>
		{:else if healthStatus === 'ok'}
			<strong>ok</strong> {version ? `(v${version})` : ''}
		{:else}
			<strong>unreachable</strong>
		{/if}
	</p>
</div>

<style>
	.dot {
		display: inline-block;
		width: 10px;
		height: 10px;
		border-radius: 50%;
		margin-right: 6px;
	}
	.green {
		background: #22c55e;
	}
	.red {
		background: #ef4444;
	}
	.gray {
		background: #9ca3af;
	}
	h1 {
		font-family: system-ui, sans-serif;
	}
	.health {
		font-family: system-ui, sans-serif;
	}
</style>
