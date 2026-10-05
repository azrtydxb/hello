import {
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { SegmentedControl } from "./components/SegmentedControl";

/**
 * The one theme switch for the whole UI: `data-theme` on <html>. The Azrty
 * tokens (and the legacy page variables aliased to them in index.css) follow
 * it. Dark is the default, as in the design; the viewer's choice is kept in
 * localStorage, which may be unavailable (private mode, blocked storage).
 */
export type Theme = "dark" | "light";

export const THEME_STORAGE_KEY = "hello.theme";

/** The stored theme, or dark when none is stored or storage is unavailable. */
export function readStoredTheme(): Theme {
  try {
    const v = window.localStorage.getItem(THEME_STORAGE_KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    // Storage blocked: fall back to the default.
  }
  return "dark";
}

function storeTheme(theme: Theme) {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Storage blocked: the choice lasts for this page view only.
  }
}

/** Sets data-theme on <html>. */
export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
}

interface ThemeContextValue {
  theme: Theme;
  setTheme: (theme: Theme) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

/** Owns the theme for everything below it and mirrors it onto <html>. */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(readStoredTheme);

  useLayoutEffect(() => applyTheme(theme), [theme]);

  const setTheme = useCallback((next: Theme) => {
    setThemeState(next);
    storeTheme(next);
  }, []);

  const value = useMemo(() => ({ theme, setTheme }), [theme, setTheme]);
  return (
    <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
  );
}

/** The current theme and its setter; throws outside <ThemeProvider>. */
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme must be used inside <ThemeProvider>");
  return ctx;
}

const THEME_OPTIONS = [
  { value: "dark", label: "Dark", icon: "moon" },
  { value: "light", label: "Light", icon: "sun" },
] as const;

/** The Dark / Light segmented control bound to the theme. */
export function ThemeToggle({ block }: { block?: boolean }) {
  const { theme, setTheme } = useTheme();
  return (
    <SegmentedControl<Theme>
      aria-label="Theme"
      options={THEME_OPTIONS}
      value={theme}
      onChange={setTheme}
      block={block}
    />
  );
}
