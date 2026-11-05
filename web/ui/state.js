export function createUIState(limit = 64) {
  const states = new Map(); let scope = '';
  return {
    get(key, create, activate = false) {
      if (states.has(key)) return states.get(key);
      // State with any local edits or outstanding request is pinned. Never
      // silently discard somebody's answer to make a newer card interactive.
      if (states.size >= limit) {
        if (!activate) return null;
        const free = [...states].find(([, s]) => !s.dirty && !s.busy && s.node?.dataset.uiExpanded !== 'true' && !s.node?.contains(document.activeElement));
        if (!free) return null;
        free[1].dispose?.(); states.delete(free[0]);
      }
      const state = create(); states.set(key, state); return state;
    },
    values: () => states.values(),
    syncScope(role, session) {
      const next = JSON.stringify([role, session]);
      if (next !== scope) { this.clear(); scope = next; }
    },
    retainSources(sources) {
      for (const [key, state] of states) if (!sources.has(`${state.context.messageIndex}:${state.context.sha256}`)) { state.dispose?.(); states.delete(key); }
    },
    clear() { for (const s of states.values()) s.dispose?.(); states.clear(); },
    size: () => states.size,
  };
}
