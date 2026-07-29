<script lang="ts">
  import { onMount, tick } from "svelte";
  import { getUIHost } from "$lib/plugins/UIHostContext.svelte";
  import PluginContainer from "./PluginContainer.svelte";
  import type { NavigationEntry, UIContribution } from "$contracts/ui-plugin-v1";
  import type { RegisteredCommand } from "$lib/commands/CommandRegistry";

  const host = getUIHost();
  let contributions = $state<UIContribution[]>([]);
  let commands = $state<ReadonlyArray<RegisteredCommand>>([]);
  let navigation = $state<ReadonlyArray<NavigationEntry>>([]);
  let loading = $state(true);
  let error = $state("");

  function syncRegistries() {
    commands = host.commands();
    navigation = host.navigationEntries();
  }

  async function reload() {
    host.destroyAll();
    contributions = [];
    await tick();
    loading = true;
    error = "";
    try {
      contributions = await host.loadContributions();
    } catch (reason) {
      error = String(reason);
    } finally {
      loading = false;
    }
  }

  function unload() {
    host.destroyAll();
    contributions = [];
  }

  onMount(() => {
    const unsubscribe = host.subscribe(syncRegistries);
    void reload();
    return unsubscribe;
  });
</script>

<section class="plugin-shell" aria-label="Trusted UI plugins">
  <div class="plugin-toolbar">
    <button data-testid="unload-plugins" onclick={unload}>Unload plugins</button>
    <button data-testid="reload-plugins" onclick={() => void reload()}>Reload plugins</button>
  </div>

  {#if loading}
    <p class="shell-status">Loading plugins…</p>
  {:else if error}
    <p class="shell-status shell-error" role="alert">{error}</p>
  {:else}
    <nav aria-label="Plugin navigation" data-testid="plugin-navigation">
      {#each navigation as entry (entry.id)}
        <a href={entry.path} data-plugin-navigation={entry.id}>{entry.label}</a>
      {/each}
    </nav>
    <div class="command-surfaces">
      <div aria-label="Linear commands">
        {#each commands as command (command.id)}
          <button data-testid={`linear-${command.id}`} onclick={() => void host.executeCommand(command.id)}>{command.label}</button>
        {/each}
      </div>
      <div aria-label="Radial commands">
        {#each commands as command (command.id)}
          <button data-testid={`radial-${command.id}`} data-command-id={command.id} onclick={() => void host.executeCommand(command.id)}>◉ {command.label}</button>
        {/each}
      </div>
    </div>
    <div class="plugin-grid">
      {#each contributions as contribution (contribution.id)}
        <PluginContainer {contribution} {host} />
      {/each}
    </div>
  {/if}
</section>

<style>
  .plugin-toolbar, .command-surfaces { display: flex; gap: 0.75rem; margin-bottom: 1rem; }
  .command-surfaces { justify-content: space-between; }
  .plugin-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr)); gap: 1rem; }
  .shell-status { padding: 1rem; }
  .shell-error { color: #f38ba8; }
</style>
