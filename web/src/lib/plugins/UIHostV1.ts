import { goto } from "$app/navigation";
import { CommandRegistry, type RegisteredCommand } from "$lib/commands/CommandRegistry";
import {
  UI_PLUGIN_API_VERSION,
  type CommandDescriptor,
  type NavigationEntry,
  type PluginCommand,
  type PluginNavigationEntry,
  type PluginSettings,
  type EventBus,
  type HttpClient,
  type ThemeTokens,
  type UIContribution,
  type UIContributionsWire,
  type UIHostV1,
  type UIPluginContext,
  type UIPluginHandle,
  type UIPluginModule,
} from "$contracts/ui-plugin-v1";

export type {
  CommandDescriptor,
  NavigationEntry,
  UIContribution,
  UIPluginContext,
  UIPluginHandle,
  UIPluginModule,
} from "$contracts/ui-plugin-v1";

const CONTRIBUTIONS_ENDPOINT = "/api/v1/ui/contributions";
export const PLUGIN_ASSET_PREFIX = "/api/v1/plugins/assets/";
const UI_TYPES = new Set(["panel", "view", "widget"]);
const DEFAULT_THEME_TOKENS: Record<string, string> = {
  "color-bg": "#1e1e2e",
  "color-bg-surface": "#313244",
  "color-text": "#cdd6f4",
  "color-accent": "#89b4fa",
  "color-error": "#f38ba8",
  radius: "0.375rem",
};

type ModuleLoader = (url: string) => Promise<unknown>;
type Navigator = (path: string) => Promise<void>;

export interface UIHostOptions {
  loadModule?: ModuleLoader;
  navigate?: Navigator;
}

interface ModuleState {
  sourceUrl: string;
  modulePromise: Promise<UIPluginModule>;
  module?: UIPluginModule;
  pending: Set<string>;
  contributions: Set<string>;
  destroyed: boolean;
}

interface MountState {
  contribution: UIContribution;
  handle: UIPluginHandle;
  scope: RegistrationScope;
}

interface RegistrationScope {
  commands: Set<string>;
  navigation: Set<string>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const actual = Object.keys(value).sort();
  return actual.length === keys.length && actual.every((key, index) => key === [...keys].sort()[index]);
}

function requiredString(value: Record<string, unknown>, key: string): string {
  const item = value[key];
  if (typeof item !== "string" || item.trim() === "") {
    throw new Error(`UI contribution ${key} must be a non-empty string`);
  }
  return item;
}

/** Reject a URL before import(), leaving no dynamic-import escape hatch. */
export function validatePluginModuleURL(candidate: string): string {
  if (typeof candidate !== "string" || candidate.trim() === "") {
    throw new Error("UI plugin sourceUrl is required");
  }
  if (candidate.startsWith("//") || /(^|[/\\])(?:\.{1,2}|%2e%2e?)(?:[/\\]|$)/i.test(candidate)) {
    throw new Error(`UI plugin sourceUrl is not an authorized asset URL: ${candidate}`);
  }
  let url: URL;
  try {
    url = new URL(candidate, window.location.origin);
  } catch {
    throw new Error(`UI plugin sourceUrl is invalid: ${candidate}`);
  }
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.origin !== window.location.origin ||
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    !url.pathname.startsWith(PLUGIN_ASSET_PREFIX)
  ) {
    throw new Error(`UI plugin sourceUrl is not an authorized asset URL: ${candidate}`);
  }
  const relative = decodeURIComponent(url.pathname.slice(PLUGIN_ASSET_PREFIX.length));
  if (relative.split("/").some((part) => part === "" || part === "." || part === "..")) {
    throw new Error(`UI plugin sourceUrl is not an authorized asset URL: ${candidate}`);
  }
  return url.pathname;
}

/** Convert the exact Go snake_case payload into the public camelCase contract. */
export function parseContributionsPayload(payload: unknown): UIContribution[] {
  if (!isRecord(payload) || !exactKeys(payload, ["version", "contributions"])) {
    throw new Error("UI contributions response has an invalid shape");
  }
  const wire = payload as unknown as UIContributionsWire;
  if (wire.version !== UI_PLUGIN_API_VERSION || !Array.isArray(wire.contributions)) {
    throw new Error("UI contributions response has an unsupported version");
  }
  const seen = new Set<string>();
  return wire.contributions.map((item, index) => {
    if (!isRecord(item) || !exactKeys(item, ["id", "label", "type", "container", "priority", "icon", "source_url", "plugin_id"])) {
      throw new Error(`UI contribution at index ${index} has an invalid shape`);
    }
    const id = requiredString(item, "id");
    const pluginId = requiredString(item, "plugin_id");
    const type = requiredString(item, "type");
    if (!UI_TYPES.has(type)) throw new Error(`UI contribution ${id} has an invalid type`);
    if (typeof item.priority !== "number" || !Number.isInteger(item.priority) || item.priority < -10000 || item.priority > 10000) {
      throw new Error(`UI contribution ${id} has an invalid priority`);
    }
    if (typeof item.container !== "string" || typeof item.icon !== "string") {
      throw new Error(`UI contribution ${id} has invalid optional fields`);
    }
    if (seen.has(id)) throw new Error(`UI contribution ${id} is duplicated`);
    seen.add(id);
    return {
      id,
      label: requiredString(item, "label"),
      type: type as UIContribution["type"],
      container: item.container,
      priority: item.priority,
      icon: item.icon,
      sourceUrl: validatePluginModuleURL(requiredString(item, "source_url")),
      pluginId,
    };
  });
}

function asPluginModule(namespace: unknown, owner: string): UIPluginModule {
  if (!isRecord(namespace) || !isRecord(namespace.default)) {
    throw new Error(`UI plugin "${owner}" must default-export a module`);
  }
  const module = namespace.default;
  if (module.id !== owner || typeof module.mount !== "function" || typeof module.destroy !== "function") {
    throw new Error(`UI plugin "${owner}" has an invalid default export`);
  }
  return module as unknown as UIPluginModule;
}

function safeHandle(handle: UIPluginHandle | undefined): UIPluginHandle {
  return handle && typeof handle.dispose === "function" ? handle : { dispose() {} };
}

export class UIHostV1Impl implements UIHostV1 {
  readonly apiVersion = UI_PLUGIN_API_VERSION;
  private readonly loadModule: ModuleLoader;
  private readonly navigateWithShell: Navigator;
  private readonly modules = new Map<string, ModuleState>();
  private readonly mounts = new Map<string, MountState>();
  private readonly pending = new Set<string>();
  private readonly commandRegistry = new CommandRegistry();
  private readonly navigation = new Map<string, NavigationEntry>();
  private readonly listeners = new Set<() => void>();
  private themeTokens: Record<string, string> = { ...DEFAULT_THEME_TOKENS };

  constructor(options: UIHostOptions = {}) {
    this.loadModule = options.loadModule ?? ((url) => import(/* @vite-ignore */ url));
    this.navigateWithShell = options.navigate ?? ((path) => goto(path));
  }

  async loadContributions(): Promise<UIContribution[]> {
    const response = await fetch(CONTRIBUTIONS_ENDPOINT);
    if (!response.ok) throw new Error(`UI contributions request failed: ${response.status}`);
    return parseContributionsPayload(await response.json());
  }

  async mountPlugin(contribution: UIContribution, container: HTMLElement): Promise<UIPluginHandle> {
    const sourceUrl = validatePluginModuleURL(contribution.sourceUrl);
    if (this.mounts.has(contribution.id) || this.pending.has(contribution.id)) {
      throw new Error(`UI contribution "${contribution.id}" is already mounted`);
    }
    this.pending.add(contribution.id);
    let moduleState = this.modules.get(contribution.pluginId);
    try {
      if (moduleState && moduleState.sourceUrl !== sourceUrl) {
        throw new Error(`UI plugin "${contribution.pluginId}" has conflicting module URLs`);
      }
      if (!moduleState) {
        moduleState = {
          sourceUrl,
          modulePromise: undefined as unknown as Promise<UIPluginModule>,
          pending: new Set(),
          contributions: new Set(),
          destroyed: false,
        };
        const sharedState = moduleState;
        sharedState.modulePromise = Promise.resolve()
          .then(() => this.loadModule(sourceUrl))
          .then((namespace) => {
            const loaded = asPluginModule(namespace, contribution.pluginId);
            sharedState.module = loaded;
            return loaded;
          });
        this.modules.set(contribution.pluginId, moduleState);
      }
      moduleState.pending.add(contribution.id);
      const pluginModule = await moduleState.modulePromise;
      const scope: RegistrationScope = { commands: new Set(), navigation: new Set() };
      try {
        const handle = safeHandle(await pluginModule.mount(container, this.contextFor(contribution, scope)));
        this.mounts.set(contribution.id, { contribution, handle, scope });
        moduleState.contributions.add(contribution.id);
        this.emit();
        return handle;
      } catch (error) {
        this.removeScope(scope);
        this.emit();
        throw new Error(`UI plugin "${contribution.pluginId}" mount failed: ${String(error)}`);
      }
    } finally {
      this.pending.delete(contribution.id);
      if (moduleState) {
        moduleState.pending.delete(contribution.id);
        this.releaseModuleState(contribution.pluginId, moduleState);
      }
    }
  }

  destroyPlugin(contributionId: string): void {
    const mount = this.mounts.get(contributionId);
    if (!mount) return;
    this.mounts.delete(contributionId);
    const state = this.modules.get(mount.contribution.pluginId);
    try { mount.handle.dispose(); } catch { /* cleanup continues */ }
    this.removeScope(mount.scope);
    if (state) {
      state.contributions.delete(contributionId);
      this.releaseModuleState(mount.contribution.pluginId, state);
    }
    this.emit();
  }

  destroyAll(): void {
    for (const id of [...this.mounts.keys()]) this.destroyPlugin(id);
  }

  mountedPluginIds(): string[] { return [...this.modules.keys()].sort(); }
  mountedContributions(): ReadonlyMap<string, UIContribution> {
    return new Map([...this.mounts].map(([id, mount]) => [id, mount.contribution]));
  }
  commands(): ReadonlyArray<RegisteredCommand> { return this.commandRegistry.list(); }
  navigationEntries(): ReadonlyArray<NavigationEntry> {
    return [...this.navigation.values()].sort((a, b) => a.priority - b.priority || a.label.localeCompare(b.label));
  }
  executeCommand(id: string): Promise<boolean> { return this.commandRegistry.execute(id); }
  subscribe(listener: () => void): () => void { this.listeners.add(listener); return () => this.listeners.delete(listener); }
  setThemeTokens(tokens: Record<string, string>): void { this.themeTokens = { ...DEFAULT_THEME_TOKENS, ...tokens }; }

  private contextFor(contribution: UIContribution, scope: RegistrationScope): UIPluginContext {
    const pluginId = contribution.pluginId;
    const settings: PluginSettings = {
      async get<T>(key: string): Promise<T | null> {
        const response = await fetch(`/api/v1/plugins/${encodeURIComponent(pluginId)}/settings/${encodeURIComponent(key)}`);
        if (response.status === 404) return null;
        if (!response.ok) throw new Error(`plugin settings read failed: ${response.status}`);
        return response.json() as Promise<T>;
      },
      async set(key: string, value: unknown): Promise<void> { await requireOK(fetch(`/api/v1/plugins/${encodeURIComponent(pluginId)}/settings/${encodeURIComponent(key)}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ value }) })); },
      async delete(key: string): Promise<void> { await requireOK(fetch(`/api/v1/plugins/${encodeURIComponent(pluginId)}/settings/${encodeURIComponent(key)}`, { method: "DELETE" })); },
      async list(): Promise<string[]> { const response = await fetch(`/api/v1/plugins/${encodeURIComponent(pluginId)}/settings`); if (!response.ok) throw new Error(`plugin settings list failed: ${response.status}`); return response.json() as Promise<string[]>; },
    };
    const events: EventBus = { async publish(type, payload) { await requireOK(fetch("/api/v1/events", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ type, payload, plugin_id: pluginId }) })); } };
    const theme: ThemeTokens = Object.freeze({ prefix: "--doublangu-", tokens: Object.freeze({ ...this.themeTokens }) });
    return {
      pluginId, contribution, settings, events, theme,
      http: { fetch: (input, init) => fetch(input, init) } as HttpClient,
      registerCommand: (command: PluginCommand) => { this.commandRegistry.register({ ...command, pluginId }); scope.commands.add(command.id); this.emit(); },
      registerNavigation: (entry: PluginNavigationEntry) => { if (this.navigation.has(entry.id)) throw new Error(`navigation "${entry.id}" is already registered`); this.navigation.set(entry.id, { ...entry, pluginId }); scope.navigation.add(entry.id); this.emit(); },
      navigate: (path: string) => this.navigateWithShell(path),
    };
  }

  private removeScope(scope: RegistrationScope): void {
    for (const id of scope.commands) this.commandRegistry.unregister(id);
    for (const id of scope.navigation) this.navigation.delete(id);
  }
  private releaseModuleState(pluginId: string, state: ModuleState): void {
    if (state.pending.size !== 0 || state.contributions.size !== 0 || this.modules.get(pluginId) !== state) return;
    this.modules.delete(pluginId);
    if (state.module && !state.destroyed) {
      state.destroyed = true;
      try { state.module.destroy(); } catch { /* cleanup continues */ }
    }
  }
  private emit(): void { for (const listener of this.listeners) listener(); }
}

async function requireOK(response: Promise<Response>): Promise<void> {
  const result = await response;
  if (!result.ok) throw new Error(`plugin API write failed: ${result.status}`);
}
