<script lang="ts">
  import { appPath } from "$lib/paths";
</script>

<div class="container">
	<p class="eyebrow">Dutch → English</p>
	<h1>Read Dutch.<br />Discover English quietly.</h1>
	<p class="subtitle">Paste an article and reveal translations only when you need them.</p>
	<a class="button" href={appPath('/reader')}>Open your articles</a>
</div>

<style>
  .container {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
		min-height: 62vh;
    text-align: center;
  }

	h1 {
		max-width: 13ch;
		margin-bottom: 0.8rem;
		font-size: clamp(2.5rem, 8vw, 5.5rem);
		line-height: 0.98;
		letter-spacing: -0.055em;
	}

	.subtitle {
		max-width: 34rem;
		color: var(--color-muted);
		margin-bottom: 1.5rem;
		font-size: 1.1rem;
	}

	.eyebrow {
		margin-bottom: 0.75rem;
		color: var(--color-accent-strong);
		font-size: 0.8rem;
		font-weight: 800;
		letter-spacing: 0.14em;
		text-transform: uppercase;
	}

	.button {
		padding: 0.7rem 1rem;
		border-radius: 0.55rem;
		background: var(--color-accent);
		color: #11131d;
		font-weight: 800;
		text-decoration: none;
	}
</style>
