import { useCallback, useEffect, useState } from "react";

// Mirrored in public/theme.js, which applies the class before first paint: doing it
// only here, in an effect, flashes the light theme on every load.
const KEY = "relaix.theme";
export type Theme = "light" | "dark";

function systemTheme(): Theme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/** An explicit choice if one was made, the system's otherwise. */
function readTheme(): Theme {
  try {
    const stored = localStorage.getItem(KEY);
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // Storage blocked: fall through to the system preference.
  }
  return systemTheme();
}

export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(readTheme);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);

  const toggle = useCallback(() => {
    setTheme((t) => {
      const next = t === "dark" ? "light" : "dark";
      try {
        // Saved only on a click, so someone who never chose keeps following the system.
        localStorage.setItem(KEY, next);
      } catch {
        // Private browsing: right for this tab, just not remembered.
      }
      return next;
    });
  }, []);

  return [theme, toggle];
}
