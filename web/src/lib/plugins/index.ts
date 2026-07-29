/**
 * web/src/lib/plugins/index.ts
 *
 * Public API surface for the UI plugin host.
 */
export { UIHostV1Impl } from "./UIHostV1";
export { setUIHost, getUIHost } from "./UIHostContext.svelte";
export { default as UIHostProvider } from "./UIHostContext.svelte";
export type {
  UIType,
  Priority,
  UIContribution,
  UIContributionWire,
  UIContributionsWire,
  CommandDescriptor,
  PluginCommand,
  NavigationEntry,
  PluginNavigationEntry,
  PluginSettings,
  EventBus,
  HttpClient,
  ThemeTokens,
  UIPluginContext,
  UIPluginModule,
  UIPluginHandle,
  UIHostV1,
} from "$contracts/ui-plugin-v1";
