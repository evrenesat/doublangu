<script lang="ts" module>
  /**
   * UIHostContext.svelte
   *
   * Svelte 5 context provider for the UI plugin host.
   * Wraps the shell layout and exposes the UIHostV1 instance
   * via Svelte's context API so child components (plugin panels,
   * command palette, navigation) can access it.
   */
  import { setContext, getContext } from "svelte";
  import { UIHostV1Impl } from "./UIHostV1";

  const UI_HOST_KEY = Symbol("doublangu-ui-host");

  /** Provide the UI host to the component tree. */
  export function setUIHost(host: UIHostV1Impl): void {
    setContext(UI_HOST_KEY, host);
  }

  /** Retrieve the UI host from context. */
  export function getUIHost(): UIHostV1Impl {
    const host = getContext<UIHostV1Impl>(UI_HOST_KEY);
    if (!host) {
      throw new Error("UIHostV1 not found in context — wrap your app in <UIHostProvider>");
    }
    return host;
  }
</script>

<script lang="ts">
  import { onMount } from "svelte";

  let { children } = $props();

  const host = new UIHostV1Impl();
	setUIHost(host);

  onMount(() => {
    return () => {
      host.destroyAll();
    };
  });
</script>

{@render children()}
