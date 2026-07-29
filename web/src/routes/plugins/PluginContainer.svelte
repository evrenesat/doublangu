<script lang="ts">
  import { onMount } from "svelte";
  import type { UIContribution } from "$contracts/ui-plugin-v1";
  import type { UIHostV1Impl } from "$lib/plugins/UIHostV1";

  interface Props { contribution: UIContribution; host: UIHostV1Impl; }
  let { contribution, host }: Props = $props();
  let container = $state<HTMLDivElement>();
  let mountError = $state("");

  onMount(() => {
    if (container) {
      void host.mountPlugin(contribution, container).catch((reason) => {
        mountError = String(reason);
      });
    }
    return () => host.destroyPlugin(contribution.id);
  });

</script>

{#snippet failed(error: unknown)}
  <div class="plugin-error" role="alert" data-testid={`render-error-${contribution.id}`}>
    {contribution.pluginId}: {String(error)}
  </div>
{/snippet}

<svelte:boundary {failed}>
  <article class="plugin-container" data-contribution-id={contribution.id}>
    <header>{contribution.label} <small>{contribution.type}</small></header>
    {#if mountError}
      <p class="plugin-error" role="alert" data-testid={`mount-error-${contribution.id}`}>{mountError}</p>
    {/if}
    <div class="plugin-content" bind:this={container}></div>
  </article>
</svelte:boundary>

<style>
  .plugin-container { border: 1px solid #45475a; border-radius: 0.375rem; min-height: 8rem; }
  header, .plugin-content, .plugin-error { padding: 0.75rem; }
  .plugin-error { color: #f38ba8; }
</style>
