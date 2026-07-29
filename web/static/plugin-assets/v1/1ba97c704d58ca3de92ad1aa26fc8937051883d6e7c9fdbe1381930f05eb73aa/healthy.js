let executions = 0;

export default {
  id: "sample-plugin",
  mount(container, context) {
    const root = document.createElement("section");
    root.className = "healthy-plugin";
    root.textContent = `Healthy ${context.contribution.id}`;
    if (context.contribution.id === "command") {
      const count = document.createElement("output");
      count.id = "command-executions";
      count.textContent = String(executions);
      root.appendChild(count);
      context.registerCommand({
        id: "sample-command",
        label: "Run sample command",
        description: "Runs the sample command",
        category: "sample",
        execute() { executions += 1; count.textContent = String(executions); },
      });
    }
    if (context.contribution.id === "radial") {
      context.registerNavigation({ id: "sample-navigation", label: "Sample navigation", icon: "", path: "/plugins", parent: "", priority: 1 });
    }
    container.appendChild(root);
    return { dispose() { root.remove(); } };
  },
  destroy() {},
};
