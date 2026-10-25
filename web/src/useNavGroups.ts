import { useCallback, useState } from "react";

const PREFIX = "hello.nav.expanded.";

function load(key: string): Set<string> {
  try {
    const raw = window.localStorage.getItem(key);
    const v: unknown = raw ? JSON.parse(raw) : [];
    return new Set(
      Array.isArray(v)
        ? v.filter((x): x is string => typeof x === "string")
        : [],
    );
  } catch {
    return new Set();
  }
}

function save(key: string, set: Set<string>) {
  try {
    window.localStorage.setItem(key, JSON.stringify([...set]));
  } catch {
    // Storage unavailable or full: the state just is not remembered.
  }
}

/**
 * Which sidebar groups are expanded, remembered per user. The group holding
 * the current route is expanded on load and whenever navigation enters it;
 * otherwise only groups the user opened are expanded.
 */
export function useNavGroups(user: string, activeLabel: string | null) {
  const key = PREFIX + user;
  const [state, setState] = useState(() => {
    const set = load(key);
    if (activeLabel) set.add(activeLabel);
    return { key, active: activeLabel, set };
  });

  let current = state;
  // Adjust during render when the user or the active group changes.
  if (state.key !== key) {
    const set = load(key);
    if (activeLabel) set.add(activeLabel);
    current = { key, active: activeLabel, set };
    setState(current);
  } else if (state.active !== activeLabel) {
    const set = new Set(state.set);
    if (activeLabel) set.add(activeLabel);
    current = { key, active: activeLabel, set };
    setState(current);
    save(key, set);
  }

  const toggle = useCallback((label: string) => {
    setState((prev) => {
      const set = new Set(prev.set);
      if (!set.delete(label)) set.add(label);
      save(prev.key, set);
      return { ...prev, set };
    });
  }, []);

  return { isExpanded: (label: string) => current.set.has(label), toggle };
}
