/**
 * web/src/lib/commands/CommandRegistry.ts
 *
 * In-memory command registry that merges plugin-contributed
 * commands with shell built-ins. Supports invocation through
 * the command palette and keyboard shortcuts.
 */

import type { CommandDescriptor } from "$contracts/ui-plugin-v1";

/** A command ready for execution, including the plugin-scoped handler. */
export interface RegisteredCommand extends CommandDescriptor {
  /** Execute the command. The handler is supplied by the plugin. */
  execute: () => void | Promise<void>;
}

/**
 * Registry that holds all commands — shell built-ins and plugin
 * contributions — and supports lookup by ID or keyboard shortcut.
 */
export class CommandRegistry {
  private _commands = new Map<string, RegisteredCommand>();

  /** Register a command. Conflicts are rejected; no command is overwritten. */
  register(cmd: RegisteredCommand): void {
		if (this._commands.has(cmd.id)) {
			throw new Error(`command "${cmd.id}" is already registered`);
		}
    this._commands.set(cmd.id, cmd);
  }

  /** Unregister by ID. */
  unregister(commandId: string): boolean {
    return this._commands.delete(commandId);
  }

  /** Unregister all commands owned by a plugin. */
  unregisterByPlugin(pluginId: string): void {
    for (const [id, cmd] of this._commands) {
      if (cmd.pluginId === pluginId) {
        this._commands.delete(id);
      }
    }
  }

  /** Get a command by ID. */
  get(commandId: string): RegisteredCommand | undefined {
    return this._commands.get(commandId);
  }

  /** Return all commands sorted by category then label. */
  list(): RegisteredCommand[] {
    return Array.from(this._commands.values()).sort((a, b) => {
      const cat = a.category.localeCompare(b.category);
      if (cat !== 0) return cat;
      return a.label.localeCompare(b.label);
    });
  }

  /** Find commands matching a search query (case-insensitive substring). */
  search(query: string): RegisteredCommand[] {
    const q = query.toLowerCase();
    return this.list().filter(
      (cmd) =>
        cmd.label.toLowerCase().includes(q) ||
        cmd.description.toLowerCase().includes(q) ||
        cmd.category.toLowerCase().includes(q),
    );
  }

  /** Execute a command by ID. Returns false if not found. */
  async execute(commandId: string): Promise<boolean> {
    const cmd = this._commands.get(commandId);
    if (!cmd) return false;
    await cmd.execute();
    return true;
  }

  /** Number of registered commands. */
  get size(): number {
    return this._commands.size;
  }
}
