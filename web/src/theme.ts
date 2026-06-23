export const THEME_STORAGE_KEY = "tifl.theme";

export const THEMES = [
  { id: "default", label: "Default" },
  { id: "paper", label: "Paper" },
  { id: "high-contrast", label: "High contrast" },
] as const;

export type ThemeID = typeof THEMES[number]["id"];

const themeIDs = new Set<string>(THEMES.map((theme) => theme.id));

export function isThemeID(value: string | null | undefined): value is ThemeID {
  return typeof value === "string" && themeIDs.has(value);
}

export function normalizeTheme(value: string | null | undefined): ThemeID {
  return isThemeID(value) ? value : "default";
}

export function applyTheme(value: string, persist = true): ThemeID {
  const theme = normalizeTheme(value);
  document.documentElement.dataset.theme = theme;
  if (persist) {
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch {
      // Storage can be unavailable in locked-down browser/WebView contexts.
      // The active document still receives the selected theme.
    }
  }
  return theme;
}
