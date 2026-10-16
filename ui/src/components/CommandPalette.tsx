import { History, Search } from "lucide-react";
import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import type { NavGroup } from "./Layout";

interface Entry {
  key: string;
  label: string;
  hint: string;
  icon: NavGroup["items"][number]["icon"];
  to: string;
}

/** Folds case and accents, so "integ" finds "Intégration". */
function fold(s: string): string {
  return s.normalize("NFD").replace(/\p{Diacritic}/gu, "").toLowerCase();
}

/**
 * Ctrl/⌘ K: go anywhere in the console, or look a number up in the history.
 * Opened by the shortcut or by the search field at the top of the sidebar.
 */
export function CommandPalette({ groups, open, onOpenChange }: { groups: NavGroup[]; open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);

  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onOpenChange(!open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onOpenChange]);

  useEffect(() => {
    if (open) {
      setQuery("");
      setIndex(0);
    }
  }, [open]);

  const entries = useMemo(() => {
    const q = fold(query.trim());
    const pages: Entry[] = groups.flatMap((g) =>
      g.items.map((i) => ({ key: i.to, label: i.label, hint: g.label, icon: i.icon, to: i.to })),
    );
    const found = q ? pages.filter((p) => fold(p.label).includes(q) || fold(p.hint).includes(q)) : pages;
    // A number, or the start of one: offer it to the history's filter.
    const digits = query.replace(/[\s.-]/g, "");
    if (/^\+?\d{3,}$/.test(digits)) {
      found.unshift({
        key: "history-search",
        label: `Rechercher ${digits} dans l'historique`,
        hint: "Messages",
        icon: History,
        to: `/history?recipient=${encodeURIComponent(digits)}`,
      });
    }
    return found;
  }, [groups, query]);

  const go = (e: Entry | undefined) => {
    if (!e) return;
    onOpenChange(false);
    navigate(e.to);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setIndex((i) => Math.min(i + 1, entries.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setIndex((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      go(entries[index]);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent showCloseButton={false} className="top-[20%] translate-y-0 gap-0 overflow-hidden p-0 shadow-drawer sm:max-w-lg">
        <DialogTitle className="sr-only">Rechercher</DialogTitle>
        <DialogDescription className="sr-only">Aller à une page, ou chercher un numéro dans l'historique.</DialogDescription>
        <div className="flex items-center gap-2 border-b px-4">
          <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <input
            autoFocus
            role="combobox"
            aria-expanded
            aria-controls="palette-list"
            aria-activedescendant={entries[index] ? `palette-${entries[index]!.key}` : undefined}
            className="h-12 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            placeholder="Aller à une page, ou un numéro…"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setIndex(0);
            }}
            onKeyDown={onKeyDown}
          />
          <kbd className="rounded border px-1.5 font-mono text-[11px] text-muted-foreground">Échap</kbd>
        </div>
        <ul id="palette-list" role="listbox" className="max-h-80 overflow-y-auto p-1.5">
          {entries.length === 0 && <li className="px-3 py-6 text-center text-sm text-muted-foreground">Rien ne correspond.</li>}
          {entries.map((e, i) => (
            <li
              key={e.key}
              id={`palette-${e.key}`}
              role="option"
              aria-selected={i === index}
              onMouseEnter={() => setIndex(i)}
              onClick={() => go(e)}
              className={cn(
                "flex cursor-pointer items-center gap-2.5 rounded-md px-3 py-2 text-sm",
                i === index ? "bg-accent text-foreground" : "text-muted-foreground",
              )}
            >
              <e.icon className={cn("size-4 shrink-0", i === index && "text-primary")} aria-hidden />
              <span className="flex-1 truncate">{e.label}</span>
              <span className="text-xs text-muted-foreground">{e.hint}</span>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  );
}
