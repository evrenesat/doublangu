import { beforeEach, describe, expect, it, vi } from "vitest";
import { parseContributionsPayload, UIHostV1Impl, validatePluginModuleURL } from "./UIHostV1";
import type { UIContribution, UIPluginModule } from "$contracts/ui-plugin-v1";

const sourceUrl = "/api/v1/plugins/assets/v1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/healthy.js";
function contribution(overrides: Partial<UIContribution> = {}): UIContribution {
  return { id: "panel", label: "Panel", type: "panel", container: "", priority: 10, icon: "", sourceUrl, pluginId: "sample-plugin", ...overrides };
}
function wire(items: unknown[]) { return { version: "v1", contributions: items }; }
function validWire(overrides: Record<string, unknown> = {}) {
  return { id: "panel", label: "Panel", type: "panel", container: "", priority: 10, icon: "", source_url: sourceUrl, plugin_id: "sample-plugin", ...overrides };
}
function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("UIHostV1Impl", () => {
  let module: UIPluginModule;
  let host: UIHostV1Impl;
  let moduleLoads: string[];

  beforeEach(() => {
    moduleLoads = [];
    module = { id: "sample-plugin", mount: vi.fn(() => ({ dispose: vi.fn() })), destroy: vi.fn() };
    host = new UIHostV1Impl({
      loadModule: async (url) => { moduleLoads.push(url); return { default: module }; },
      navigate: async () => {},
    });
  });

  it("parses the exact Go payload and rejects unknown, duplicate, or invalid descriptors", () => {
    expect(parseContributionsPayload(wire([validWire()]))).toEqual([contribution()]);
    expect(() => parseContributionsPayload(wire([validWire(), validWire({ label: "Duplicate" })]))).toThrow("duplicated");
    expect(() => parseContributionsPayload(wire([validWire({ type: "route" })]))).toThrow("invalid type");
    expect(() => parseContributionsPayload(wire([validWire({ priority: 1.5 })]))).toThrow("invalid priority");
    expect(() => parseContributionsPayload({ version: "v1", contributions: [], unexpected: true })).toThrow("invalid shape");
  });

  it("rejects data, cross-origin, credentials, traversal, query, and fragment URLs before import", () => {
    for (const bad of ["data:text/javascript,export default {}", "https://other.example/plugin.js", "//localhost/plugin.js", "/api/v1/plugins/assets/v1/x/../module.js", "/api/v1/plugins/assets/v1/a/module.js?x=1", "/api/v1/plugins/assets/v1/a/module.js#x", "https://user:pass@localhost/plugin.js"]) {
      expect(() => validatePluginModuleURL(bad)).toThrow("authorized asset URL");
    }
    expect(moduleLoads).toEqual([]);
  });

  it("redirects unauthenticated contribution loads to sign-in without surfacing a plugin error", async () => {
    const navigate = vi.fn(async () => {});
    host = new UIHostV1Impl({
      loadModule: async (url) => { moduleLoads.push(url); return { default: module }; },
      navigate,
    });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(
      JSON.stringify({ error: "authentication required", code: "v1.authentication_error" }),
      { status: 401, headers: { "Content-Type": "application/json" } },
    )));

    await expect(host.loadContributions()).resolves.toEqual([]);
    expect(navigate).toHaveBeenCalledOnce();
    expect(navigate).toHaveBeenCalledWith("/login");
    expect(moduleLoads).toEqual([]);
    vi.unstubAllGlobals();
  });

  it("retains material contribution request errors other than authentication", async () => {
    const navigate = vi.fn(async () => {});
    host = new UIHostV1Impl({ navigate });
    vi.stubGlobal("fetch", vi.fn(async () => new Response("unavailable", { status: 503 })));

    await expect(host.loadContributions()).rejects.toThrow("UI contributions request failed: 503");
    expect(navigate).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("shares one deferred load across concurrent contributions and destroys after either final handle", async () => {
    const loaded = deferred<unknown>();
    host = new UIHostV1Impl({
      loadModule: async (url) => { moduleLoads.push(url); return loaded.promise; },
      navigate: async () => {},
    });
    const firstMount = host.mountPlugin(contribution(), document.createElement("div"));
    const secondMount = host.mountPlugin(contribution({ id: "reader", type: "view" }), document.createElement("div"));
    await vi.waitFor(() => expect(moduleLoads).toHaveLength(1));
    loaded.resolve({ default: module });
    const [first, second] = await Promise.all([firstMount, secondMount]);
    expect(host.mountedPluginIds()).toEqual(["sample-plugin"]);
    host.destroyPlugin("reader");
    expect(module.destroy).not.toHaveBeenCalled();
    host.destroyPlugin("panel");
    expect(module.destroy).toHaveBeenCalledOnce();
    first.dispose(); second.dispose();
  });

  it("rejects a conflicting source URL during a shared load without disturbing its sibling", async () => {
    const loaded = deferred<unknown>();
    host = new UIHostV1Impl({
      loadModule: async (url) => { moduleLoads.push(url); return loaded.promise; },
      navigate: async () => {},
    });
    const healthy = host.mountPlugin(contribution(), document.createElement("div"));
    await vi.waitFor(() => expect(moduleLoads).toHaveLength(1));
    await expect(host.mountPlugin(contribution({ id: "other", sourceUrl: sourceUrl.replace("healthy", "other") }), document.createElement("div"))).rejects.toThrow("conflicting module URLs");
    loaded.resolve({ default: module });
    await healthy;
    expect(moduleLoads).toHaveLength(1);
    expect(host.mountedContributions().size).toBe(1);
  });

  it("rejects mismatched module IDs without retaining plugin state", async () => {
    const mismatch = new UIHostV1Impl({ loadModule: async () => ({ default: { ...module, id: "other-plugin" } }), navigate: async () => {} });
    await expect(mismatch.mountPlugin(contribution(), document.createElement("div"))).rejects.toThrow("invalid default export");
    expect(mismatch.mountedPluginIds()).toEqual([]);
  });

  it("keeps a healthy concurrent mount when its sibling mount fails", async () => {
    module.mount = vi.fn((_container, context) => {
      if (context.contribution.id === "broken") throw new Error("broken mount");
      context.registerCommand({ id: "healthy-command", label: "Healthy", description: "Runs", category: "sample", execute: vi.fn() });
      return { dispose() {} };
    });
    const healthy = host.mountPlugin(contribution(), document.createElement("div"));
    const broken = host.mountPlugin(contribution({ id: "broken" }), document.createElement("div"));
    await healthy;
    await expect(broken).rejects.toThrow("broken mount");
    expect(moduleLoads).toHaveLength(1);
    expect(host.mountedPluginIds()).toEqual(["sample-plugin"]);
    expect([...host.mountedContributions().keys()]).toEqual(["panel"]);
    await expect(host.executeCommand("healthy-command")).resolves.toBe(true);
    expect(module.destroy).not.toHaveBeenCalled();
  });

  it("removes only one contribution's command and navigation scope", async () => {
    module.mount = vi.fn((_container, context) => {
      context.registerCommand({ id: `${context.contribution.id}-command`, label: context.contribution.label, description: "Runs", category: "sample", execute: vi.fn() });
      context.registerNavigation({ id: `${context.contribution.id}-navigation`, label: context.contribution.label, icon: "", path: `/plugins/${context.contribution.id}`, parent: "", priority: context.contribution.priority });
      return { dispose() {} };
    });
    await Promise.all([
      host.mountPlugin(contribution(), document.createElement("div")),
      host.mountPlugin(contribution({ id: "reader", label: "Reader", type: "view" }), document.createElement("div")),
    ]);
    host.destroyPlugin("panel");
    expect(host.commands().map((command) => command.id)).toEqual(["reader-command"]);
    expect(host.navigationEntries().map((entry) => entry.id)).toEqual(["reader-navigation"]);
    expect(host.mountedPluginIds()).toEqual(["sample-plugin"]);
    expect(module.destroy).not.toHaveBeenCalled();
  });

  it("cleans up a rejected shared load and permits a clean retry", async () => {
    const firstLoad = deferred<unknown>();
    let calls = 0;
    host = new UIHostV1Impl({
      loadModule: async () => {
        calls += 1;
        if (calls === 1) return firstLoad.promise;
        return { default: module };
      },
      navigate: async () => {},
    });
    const first = host.mountPlugin(contribution(), document.createElement("div"));
    const second = host.mountPlugin(contribution({ id: "reader" }), document.createElement("div"));
    await vi.waitFor(() => expect(calls).toBe(1));
    firstLoad.reject(new Error("load failed"));
    await expect(Promise.allSettled([first, second])).resolves.toMatchObject([
      { status: "rejected" },
      { status: "rejected" },
    ]);
    expect(host.mountedPluginIds()).toEqual([]);
    expect(host.mountedContributions().size).toBe(0);
    expect(host.commands()).toEqual([]);
    expect(host.navigationEntries()).toEqual([]);
    await host.mountPlugin(contribution(), document.createElement("div"));
    expect(calls).toBe(2);
  });

  it("continues exact scope and final-state cleanup when dispose and destroy throw", async () => {
    module.mount = vi.fn((_container, context) => {
      context.registerCommand({ id: "sample-command", label: "Sample", description: "Runs", category: "sample", execute: vi.fn() });
      context.registerNavigation({ id: "sample-navigation", label: "Sample", icon: "", path: "/plugins/sample", parent: "", priority: 10 });
      return { dispose() { throw new Error("dispose failed"); } };
    });
    module.destroy = vi.fn(() => { throw new Error("destroy failed"); });
    await host.mountPlugin(contribution(), document.createElement("div"));
    host.destroyPlugin("panel");
    expect(host.commands()).toEqual([]);
    expect(host.navigationEntries()).toEqual([]);
    expect(host.mountedPluginIds()).toEqual([]);
    expect(host.mountedContributions().size).toBe(0);
    expect(module.destroy).toHaveBeenCalledOnce();
    await host.mountPlugin(contribution(), document.createElement("div"));
    expect(moduleLoads).toHaveLength(2);
  });

  it("freezes theme snapshots, escapes settings paths, and rejects failed writes", async () => {
    let context: Parameters<UIPluginModule["mount"]>[1] | undefined;
    module.mount = vi.fn((_container, received) => { context = received; return { dispose() {} }; });
    await host.mountPlugin(contribution(), document.createElement("div"));
    expect(context).toBeDefined();
    expect(Object.isFrozen(context!.theme)).toBe(true);
    expect(Object.isFrozen(context!.theme.tokens)).toBe(true);
    let requested = "";
    vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
      requested = String(input);
      return new Response("denied", { status: 403 });
    });
    await expect(context!.settings.set("key/with space", "value")).rejects.toThrow("write failed: 403");
    expect(requested).toContain("key%2Fwith%20space");
    vi.unstubAllGlobals();
  });
});
