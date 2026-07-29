<script lang="ts">
  import favicon from "$lib/assets/favicon.svg";
  import UIHostProvider from "$lib/plugins/UIHostContext.svelte";

  let { children } = $props();
</script>

<svelte:head>
  <link rel="icon" href={favicon} />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
</svelte:head>

<UIHostProvider>
  <header>
    <nav>
      <a class="brand" href="/">Doublangu</a>
      <a href="/plugins">Plugins</a>
    </nav>
  </header>

  <main>
    {@render children()}
  </main>
</UIHostProvider>

<style>
  header {
    background: var(--color-bg-header, #1e1e2e);
    padding: 0.75rem 1rem;
  }

  nav {
	max-width: 80rem;
	margin: 0 auto;
	display: flex;
	gap: 1rem;
	align-items: center;
  }

  .brand {
    font-size: 1.25rem;
    font-weight: 700;
    color: var(--color-text, #cdd6f4);
  }

  main {
    max-width: 80rem;
    margin: 0 auto;
    padding: 1rem;
  }
</style>
