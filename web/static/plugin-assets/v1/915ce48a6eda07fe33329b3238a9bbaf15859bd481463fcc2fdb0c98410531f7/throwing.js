export default {
  id: "throwing-plugin",
  mount() { throw new Error("deliberate fixture failure"); },
  destroy() {},
};
