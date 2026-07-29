/** The versioned public contract for trusted same-origin UI plugins. */
export const UI_PLUGIN_API_VERSION = "v1";

export type UIType = "panel" | "view" | "widget";
export type Priority = number;

export interface UIContribution {
  id: string;
  label: string;
  type: UIType;
  container: string;
  priority: Priority;
  icon: string;
  sourceUrl: string;
  pluginId: string;
}

/** Exact snake_case payload emitted by the Go contribution endpoint. */
export interface UIContributionWire {
  id: string;
  label: string;
  type: UIType;
  container: string;
  priority: number;
  icon: string;
  source_url: string;
  plugin_id: string;
}

export interface UIContributionsWire {
  version: typeof UI_PLUGIN_API_VERSION;
  contributions: UIContributionWire[];
}

export interface CommandDescriptor {
  id: string;
  label: string;
  description: string;
  category: string;
  pluginId: string;
  shortcut?: string;
}

export type PluginCommand = Omit<CommandDescriptor, "pluginId"> & {
  execute: () => void | Promise<void>;
};

export interface NavigationEntry {
  id: string;
  label: string;
  icon: string;
  path: string;
  parent: string;
  priority: Priority;
  pluginId: string;
}

export type PluginNavigationEntry = Omit<NavigationEntry, "pluginId">;

export interface PluginSettings {
  get<T = unknown>(key: string): Promise<T | null>;
  set(key: string, value: unknown): Promise<void>;
  delete(key: string): Promise<void>;
  list(): Promise<string[]>;
}

export interface EventBus {
  publish<T = unknown>(type: string, payload: T): Promise<void>;
}

export interface HttpClient {
  fetch(input: RequestInfo, init?: RequestInit): Promise<Response>;
}

export interface ThemeTokens {
  readonly prefix: string;
  readonly tokens: Readonly<Record<string, string>>;
}

export interface UIPluginContext {
  readonly pluginId: string;
  readonly contribution: UIContribution;
  readonly settings: PluginSettings;
  readonly events: EventBus;
  readonly http: HttpClient;
  readonly theme: ThemeTokens;
  registerCommand(command: PluginCommand): void;
  registerNavigation(entry: PluginNavigationEntry): void;
  navigate(path: string): Promise<void>;
}

export interface UIPluginHandle {
  dispose(): void;
}

/** A loaded ESM namespace must default-export exactly this object shape. */
export interface UIPluginModule {
  id: string;
  mount(
    container: HTMLElement,
    context: UIPluginContext,
  ): UIPluginHandle | undefined | Promise<UIPluginHandle | undefined>;
  destroy(): void;
}

export interface UIHostV1 {
  readonly apiVersion: typeof UI_PLUGIN_API_VERSION;
  loadContributions(): Promise<UIContribution[]>;
  mountPlugin(contribution: UIContribution, container: HTMLElement): Promise<UIPluginHandle>;
  destroyPlugin(contributionId: string): void;
  destroyAll(): void;
  mountedPluginIds(): string[];
  mountedContributions(): ReadonlyMap<string, UIContribution>;
}
