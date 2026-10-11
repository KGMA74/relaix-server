const dateTime = new Intl.DateTimeFormat("fr-FR", { dateStyle: "short", timeStyle: "medium" });
const relative = new Intl.RelativeTimeFormat("fr-FR", { numeric: "auto" });

export function formatDateTime(iso?: string): string {
  if (!iso) return "—";
  return dateTime.format(new Date(iso));
}

export function formatRelative(iso?: string): string {
  if (!iso) return "jamais";
  const seconds = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
  const abs = Math.abs(seconds);
  if (abs < 60) return relative.format(seconds, "second");
  if (abs < 3600) return relative.format(Math.round(seconds / 60), "minute");
  if (abs < 86400) return relative.format(Math.round(seconds / 3600), "hour");
  return relative.format(Math.round(seconds / 86400), "day");
}

export function shortId(id?: string): string {
  return id ? id.slice(0, 8) : "—";
}

/** Converts a <input type="datetime-local"> value to RFC 3339 in UTC. */
export function localInputToISO(value: string): string | undefined {
  if (!value) return undefined;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString().replace(/\.\d{3}Z$/, "Z");
}
