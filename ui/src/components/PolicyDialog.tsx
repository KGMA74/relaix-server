import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, Check, Gauge, Globe2, Loader2, Plus, Regex, Search, Trash2, X } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { api } from "../api/client";
import type { AcceptRules, Availability, Country, Device, Policy, SendLimits, SimPolicy, TimeWindow, UsageCount } from "../api/types";
import { ErrorNote, Modal } from "./common";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

type Draft = Omit<Policy, "version" | "updatedAt">;

const DAYS = [
  { n: 1, short: "L", long: "lundi" },
  { n: 2, short: "M", long: "mardi" },
  { n: 3, short: "M", long: "mercredi" },
  { n: 4, short: "J", long: "jeudi" },
  { n: 5, short: "V", long: "vendredi" },
  { n: 6, short: "S", long: "samedi" },
  { n: 7, short: "D", long: "dimanche" },
];

const regionNames = (() => {
  try {
    return new Intl.DisplayNames(["fr"], { type: "region" });
  } catch {
    return null;
  }
})();
const countryName = (code: string) => regionNames?.of(code) ?? code;

const zones: string[] = (() => {
  try {
    return (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf("timeZone");
  } catch {
    return [];
  }
})();

const toHHMM = (m: number) => `${String(Math.floor(m / 60) % 24).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
const fromHHMM = (s: string) => {
  const [h, m] = s.split(":").map(Number);
  return (h ?? 0) * 60 + (m ?? 0);
};

/** Mirrors the server's matching, for the "test a number" field only. */
function accepts(r: AcceptRules, number: string, countries: Country[]): boolean | "invalid" {
  const digits = number.replace(/^\+/, "");
  if (r.countries?.length) {
    const ok = r.countries.some((c) => countries.find((x) => x.code === c)?.prefixes.some((p) => digits.startsWith(p)));
    if (!ok) return false;
  }
  if (r.pattern) {
    try {
      if (!new RegExp(r.pattern).test(number)) return false;
    } catch {
      return "invalid";
    }
  }
  return true;
}

function patternError(pattern?: string): string | null {
  if (!pattern) return null;
  try {
    new RegExp(pattern);
    return null;
  } catch (e) {
    return e instanceof Error ? e.message : "expression invalide";
  }
}

/** The rules editor for one device: its own rules, then one tab per SIM. */
export function PolicyDialog({ device, onClose }: { device: Device; onClose: () => void }) {
  const queryClient = useQueryClient();
  const countries = useQuery({ queryKey: ["countries"], queryFn: api.countries, staleTime: Infinity });
  const list = countries.data?.countries ?? [];

  const [draft, setDraft] = useState<Draft>(() => ({
    availability: device.policy?.availability ?? {},
    accept: device.policy?.accept ?? {},
    sims: device.policy?.sims ?? [],
    limits: device.policy?.limits ?? {},
  }));

  const sims = [...(device.sims ?? [])].sort((a, b) => a.slotIndex - b.slotIndex);
  const simDraft = (sub: number): SimPolicy =>
    draft.sims?.find((s) => s.subscriptionId === sub) ?? { subscriptionId: sub, accept: {}, availability: {} };
  const setSim = (next: SimPolicy) =>
    setDraft((d) => ({ ...d, sims: [...(d.sims ?? []).filter((s) => s.subscriptionId !== next.subscriptionId), next] }));

  const badLimits = (l?: SendLimits) => !!l?.perHour && !!l?.perDay && l.perDay < l.perHour;
  const invalid =
    patternError(draft.accept.pattern) !== null ||
    badLimits(draft.limits) ||
    (draft.sims ?? []).some((s) => patternError(s.accept.pattern) !== null || badLimits(s.limits));

  const save = useMutation({
    mutationFn: () => {
      // A SIM entry that restricts nothing is dropped rather than stored.
      const sims = (draft.sims ?? []).filter(
        (s) =>
          s.disabled ||
          s.accept.countries?.length ||
          s.accept.pattern ||
          s.availability.windows?.length ||
          s.limits?.perHour ||
          s.limits?.perDay,
      );
      return api.putPolicy(device.deviceId, { ...draft, sims });
    },
    onSuccess: (p) => {
      queryClient.invalidateQueries({ queryKey: ["devices"] });
      toast.success("Règles enregistrées", {
        description: device.connected ? `Version ${p.version}, transmise au téléphone.` : `Version ${p.version}. Le téléphone la recevra à sa reconnexion.`,
      });
      onClose();
    },
  });

  return (
    <Modal onClose={onClose} wide label={`Règles de ${device.label}`}>
      <div className="pr-8">
        <h2 className="text-lg font-semibold">Règles d'envoi</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          {device.label || "Ce téléphone"} ne reçoit que les messages que ces règles autorisent. Laissé vide, un champ ne restreint
          rien.
        </p>
      </div>

      <Tabs defaultValue="device" className="mt-4">
        <TabsList className="w-full justify-start">
          <TabsTrigger value="device">Téléphone</TabsTrigger>
          {sims.map((s) => (
            <TabsTrigger key={s.subscriptionId} value={`sim-${s.subscriptionId}`}>
              SIM {s.slotIndex + 1}
              {simDraft(s.subscriptionId).disabled && <span className="ml-1 text-xs text-muted-foreground">(off)</span>}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="device" className="mt-4 space-y-6">
          <AvailabilityEditor
            value={draft.availability}
            onChange={(availability) => setDraft((d) => ({ ...d, availability }))}
            phoneZone={device.timeZone}
          />
          <AcceptEditor value={draft.accept} onChange={(accept) => setDraft((d) => ({ ...d, accept }))} countries={list} />
          <LimitsEditor
            value={draft.limits ?? {}}
            onChange={(limits) => setDraft((d) => ({ ...d, limits }))}
            usage={device.usage}
            hint="Toutes SIM confondues. Au plafond, le téléphone n'est plus choisi jusqu'à ce que la fenêtre glisse."
          />
        </TabsContent>

        {sims.map((s) => {
          const sp = simDraft(s.subscriptionId);
          return (
            <TabsContent key={s.subscriptionId} value={`sim-${s.subscriptionId}`} className="mt-4 space-y-6">
              <div className="flex items-center justify-between gap-4 rounded-md border bg-muted/40 p-3">
                <div>
                  <div className="text-sm font-medium">
                    {s.carrier || s.displayName || "SIM"} {s.phoneNumber && <span className="font-mono text-muted-foreground">{s.phoneNumber}</span>}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    S'ajoute aux règles du téléphone : un message doit satisfaire les deux.
                  </div>
                </div>
                <label className="flex items-center gap-2 text-sm">
                  Utiliser cette SIM
                  <Switch checked={!sp.disabled} onCheckedChange={(on) => setSim({ ...sp, disabled: !on })} />
                </label>
              </div>
              <fieldset disabled={sp.disabled} className={cn("space-y-6", sp.disabled && "opacity-50")}>
                <AvailabilityEditor
                  value={sp.availability}
                  onChange={(availability) => setSim({ ...sp, availability })}
                  phoneZone={device.timeZone}
                  inherits
                />
                <AcceptEditor value={sp.accept} onChange={(accept) => setSim({ ...sp, accept })} countries={list} />
                <LimitsEditor
                  value={sp.limits ?? {}}
                  onChange={(limits) => setSim({ ...sp, limits })}
                  usage={device.usage?.sims?.[String(s.subscriptionId)]}
                  hint="Plafond propre à cette SIM : au-delà, l'autre SIM prend le relais si ses règles le permettent."
                />
              </fieldset>
            </TabsContent>
          );
        })}
      </Tabs>

      <div className="mt-6">
        <ErrorNote error={save.error} />
      </div>
      <div className="mt-4 flex justify-end gap-2">
        <button className="btn-secondary" onClick={onClose}>
          Annuler
        </button>
        <button className="btn-primary" onClick={() => save.mutate()} disabled={save.isPending || invalid}>
          {save.isPending ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Check className="size-4" aria-hidden />}
          Enregistrer
        </button>
      </div>
    </Modal>
  );
}

function Section({ icon: Icon, title, hint, children }: { icon: typeof Globe2; title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section>
      <div className="mb-3 flex items-start gap-2">
        <Icon className="mt-0.5 size-4 text-muted-foreground" aria-hidden />
        <div>
          <h3 className="text-sm font-semibold">{title}</h3>
          {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
        </div>
      </div>
      {children}
    </section>
  );
}

function AvailabilityEditor({
  value,
  onChange,
  phoneZone,
  inherits,
}: {
  value: Availability;
  onChange: (v: Availability) => void;
  phoneZone?: string;
  inherits?: boolean;
}) {
  const windows = value.windows ?? [];
  const always = windows.length === 0;
  const setWindows = (w: TimeWindow[]) => onChange({ ...value, windows: w });
  const update = (i: number, w: TimeWindow) => setWindows(windows.map((x, j) => (j === i ? w : x)));

  return (
    <Section
      icon={CalendarClock}
      title="Disponibilité"
      hint={
        inherits
          ? "Plages propres à cette SIM, en plus de celles du téléphone."
          : "Hors de ces plages, le téléphone ne reçoit rien ; les messages en file attendent la prochaine."
      }
    >
      <label className="mb-3 flex items-center gap-2 text-sm">
        <Switch
          checked={always}
          onCheckedChange={(on) => setWindows(on ? [] : [{ days: [1, 2, 3, 4, 5], start: 8 * 60, end: 20 * 60 }])}
        />
        {inherits ? "Mêmes horaires que le téléphone" : "Toujours disponible"}
      </label>

      {!always && (
        <div className="space-y-2">
          {windows.map((w, i) => (
            <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-2">
              <div className="flex gap-1" role="group" aria-label="Jours">
                {DAYS.map((d) => {
                  const on = !w.days?.length || w.days.includes(d.n);
                  return (
                    <button
                      key={d.n}
                      type="button"
                      title={d.long}
                      aria-pressed={on}
                      onClick={() => {
                        const current = w.days?.length ? w.days : DAYS.map((x) => x.n);
                        const next = on ? current.filter((x) => x !== d.n) : [...current, d.n].sort();
                        update(i, { ...w, days: next.length === 7 ? [] : next });
                      }}
                      className={cn(
                        "size-7 rounded-full text-xs font-medium transition-colors",
                        on ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground hover:bg-accent",
                      )}
                    >
                      {d.short}
                    </button>
                  );
                })}
              </div>
              <div className="ml-auto flex items-center gap-1.5 text-sm">
                <input
                  type="time"
                  className="input h-8 w-auto"
                  value={toHHMM(w.start)}
                  onChange={(e) => update(i, { ...w, start: fromHHMM(e.target.value) })}
                  aria-label="Début"
                />
                <span className="text-muted-foreground">→</span>
                <input
                  type="time"
                  className="input h-8 w-auto"
                  value={toHHMM(w.end)}
                  onChange={(e) => update(i, { ...w, end: fromHHMM(e.target.value) })}
                  aria-label="Fin"
                />
                <button
                  type="button"
                  className="rounded-md p-1.5 text-muted-foreground hover:bg-accent hover:text-destructive"
                  onClick={() => setWindows(windows.filter((_, j) => j !== i))}
                  aria-label="Supprimer la plage"
                >
                  <Trash2 className="size-4" />
                </button>
              </div>
              {w.end <= w.start && !(w.start === 0 && w.end === 0) && (
                <p className="w-full text-xs text-muted-foreground">Passe minuit : se termine le lendemain à {toHHMM(w.end)}.</p>
              )}
            </div>
          ))}
          <button
            type="button"
            className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
            onClick={() => setWindows([...windows, { days: [], start: 8 * 60, end: 20 * 60 }])}
          >
            <Plus className="size-4" aria-hidden /> Ajouter une plage
          </button>

          {!inherits && (
            <div className="pt-2">
              <label className="label" htmlFor="tz">
                Fuseau horaire
              </label>
              <input
                id="tz"
                className="input"
                list="tz-list"
                placeholder={phoneZone ? `${phoneZone} (celui du téléphone)` : "UTC"}
                value={value.timeZone ?? ""}
                onChange={(e) => onChange({ ...value, timeZone: e.target.value })}
              />
              <datalist id="tz-list">
                {zones.map((z) => (
                  <option key={z} value={z} />
                ))}
              </datalist>
            </div>
          )}
        </div>
      )}
    </Section>
  );
}

function AcceptEditor({
  value,
  onChange,
  countries,
}: {
  value: AcceptRules;
  onChange: (v: AcceptRules) => void;
  countries: Country[];
}) {
  const [search, setSearch] = useState("");
  const [test, setTest] = useState("");
  const selected = value.countries ?? [];
  const err = patternError(value.pattern);

  const matches = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return [];
    return countries
      .filter((c) => !selected.includes(c.code))
      .filter(
        (c) =>
          c.code.toLowerCase().startsWith(q) ||
          countryName(c.code).toLowerCase().includes(q) ||
          c.prefixes.some((p) => ("+" + p).startsWith(q.startsWith("+") ? q : "+" + q)),
      )
      .slice(0, 8);
  }, [search, countries, selected]);

  const verdict = test.trim() ? accepts(value, test.trim(), countries) : null;

  return (
    <Section icon={Globe2} title="Destinataires acceptés" hint="Un message doit venir d'un pays coché et correspondre au motif.">
      <div className="space-y-3">
        <div>
          <div className="mb-2 flex flex-wrap gap-1.5">
            {selected.length === 0 && <span className="text-sm text-muted-foreground">Tous les pays</span>}
            {selected.map((c) => (
              <span key={c} className="inline-flex items-center gap-1 rounded-full border bg-muted py-0.5 pr-1 pl-2.5 text-xs font-medium">
                {countryName(c)}
                <span className="font-mono text-muted-foreground">+{countries.find((x) => x.code === c)?.prefixes[0]}</span>
                <button
                  type="button"
                  className="rounded-full p-0.5 hover:bg-accent"
                  onClick={() => onChange({ ...value, countries: selected.filter((x) => x !== c) })}
                  aria-label={`Retirer ${countryName(c)}`}
                >
                  <X className="size-3" />
                </button>
              </span>
            ))}
          </div>
          <div className="relative">
            <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden />
            <input
              className="input pl-8"
              placeholder="Ajouter un pays (nom, code ou +indicatif)"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            {matches.length > 0 && (
              <ul className="absolute z-10 mt-1 w-full overflow-hidden rounded-md border bg-popover shadow-surface">
                {matches.map((c) => (
                  <li key={c.code}>
                    <button
                      type="button"
                      className="flex w-full items-center justify-between px-3 py-2 text-left text-sm hover:bg-accent"
                      onClick={() => {
                        onChange({ ...value, countries: [...selected, c.code] });
                        setSearch("");
                      }}
                    >
                      <span>
                        {countryName(c.code)} <span className="text-xs text-muted-foreground">{c.code}</span>
                      </span>
                      <span className="font-mono text-xs text-muted-foreground">+{c.prefixes.join(", +")}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
          {selected.some((c) => countries.find((x) => x.code === c)?.prefixes.includes("1")) && (
            <p className="mt-1 text-xs text-muted-foreground">
              États-Unis et Canada partagent l'indicatif +1 : autoriser l'un autorise l'autre.
            </p>
          )}
        </div>

        <div>
          <label className="label flex items-center gap-1.5">
            <Regex className="size-3.5 text-muted-foreground" aria-hidden /> Motif du numéro (expression régulière)
          </label>
          <input
            className={cn("input font-mono", err && "border-destructive")}
            placeholder={"^\\+2267  (vide : tout numéro)"}
            value={value.pattern ?? ""}
            onChange={(e) => onChange({ ...value, pattern: e.target.value })}
          />
          {err && <p className="mt-1 text-xs text-destructive">{err}</p>}
        </div>

        <div className="rounded-md border bg-muted/40 p-2.5">
          <label className="mb-1 block text-xs font-medium text-muted-foreground">Tester un numéro</label>
          <div className="flex items-center gap-2">
            <input className="input h-8 font-mono" placeholder="+22670000000" value={test} onChange={(e) => setTest(e.target.value)} />
            {verdict === true && <span className="text-sm font-medium whitespace-nowrap text-success">Accepté</span>}
            {verdict === false && <span className="text-sm font-medium whitespace-nowrap text-destructive">Refusé</span>}
          </div>
        </div>
      </div>
    </Section>
  );
}

/** The send caps: how much the device or SIM may be handed, with what it was handed. */
function LimitsEditor({
  value,
  onChange,
  usage,
  hint,
}: {
  value: SendLimits;
  onChange: (v: SendLimits) => void;
  usage?: UsageCount;
  hint: string;
}) {
  const field = (key: keyof SendLimits, label: string, used?: number) => (
    <div>
      <label className="label">{label}</label>
      <input
        type="number"
        min={0}
        inputMode="numeric"
        className="input"
        placeholder="Illimité"
        value={value[key] || ""}
        onChange={(e) => {
          const n = Math.max(0, Math.floor(Number(e.target.value) || 0));
          onChange({ ...value, [key]: n || undefined });
        }}
      />
      {used !== undefined && (
        <p className={cn("mt-1 text-xs", value[key] && used >= (value[key] ?? 0) ? "text-warning" : "text-muted-foreground")}>
          {used} envoyé{used > 1 ? "s" : ""}
          {value[key] ? ` sur ${value[key]}` : ""}
        </p>
      )}
    </div>
  );
  const inverted = !!value.perHour && !!value.perDay && value.perDay < value.perHour;
  return (
    <Section icon={Gauge} title="Plafonds d'envoi" hint={hint}>
      <div className="grid gap-3 sm:grid-cols-2">
        {field("perHour", "Par heure (60 min glissantes)", usage?.lastHour ?? 0)}
        {field("perDay", "Par jour (24 h glissantes)", usage?.lastDay ?? 0)}
      </div>
      {inverted && <p className="mt-2 text-xs text-destructive">Le plafond par jour ne peut pas être sous le plafond par heure.</p>}
    </Section>
  );
}
