import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BatteryCharging, BatteryMedium, Check, Copy, Loader2, MessageSquare, Plus, Power, RefreshCw, Send, Signal, SlidersHorizontal, Smartphone, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { Device } from "../api/types";
import { ConfirmDialog, Empty, ErrorNote, Modal, ModalActions, PageHeader, Pill, SignalBars, type Tone } from "../components/common";
import { PolicyDialog } from "../components/PolicyDialog";
import { formatDateTime, formatRelative, shortId } from "../lib/format";
import { useLiveEvent, usePollInterval } from "@/lib/live";
import { cn } from "@/lib/utils";

export function DevicesPage() {
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices, refetchInterval: usePollInterval(10_000) });
  const [enrolling, setEnrolling] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const all = devices.data?.devices ?? [];
  const online = all.filter((d) => d.connected);
  const offline = all.filter((d) => !d.connected);
  // Problems first: a phone off, then one on but not ready, then the rest.
  const list = (filter === "online" ? online : filter === "offline" ? offline : all)
    .slice()
    .sort((a, b) => severity(b) - severity(a) || (a.label || a.deviceId).localeCompare(b.label || b.deviceId));
  const filters: { id: Filter; label: string; n: number }[] = [
    { id: "all", label: "Tous", n: all.length },
    { id: "online", label: "En ligne", n: online.length },
    { id: "offline", label: "Hors ligne", n: offline.length },
  ];

  return (
    <>
      <PageHeader
        title="Appareils"
        subtitle="Les téléphones Android qui envoient les SMS de la passerelle."
        actions={
          <button className="btn-primary" onClick={() => setEnrolling(true)}>
            <Plus className="size-4" aria-hidden />
            Ajouter un téléphone
          </button>
        }
      />

      <ErrorNote error={devices.error} />

      {devices.isSuccess && all.length === 0 && !enrolling && <Onboarding />}

      {all.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div role="group" aria-label="Filtrer" className="inline-flex rounded-md border bg-card p-0.5">
            {filters.map((f) => (
              <button
                key={f.id}
                type="button"
                aria-pressed={filter === f.id}
                onClick={() => setFilter(f.id)}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-sm px-3 py-1 text-sm font-medium transition-colors",
                  filter === f.id ? "bg-accent text-foreground shadow-[inset_0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {f.label}
                <span className="font-mono text-xs text-muted-foreground">{f.n}</span>
              </button>
            ))}
          </div>
          <span className="text-xs text-muted-foreground">Trié par : problème d'abord</span>
        </div>
      )}

      {all.length > 0 && list.length === 0 && (
        <div className="card">
          <Empty icon={<Smartphone />}>{filter === "online" ? "Aucun téléphone en ligne." : "Aucun téléphone hors ligne."}</Empty>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-2">
        {list.map((d) => (
          <DeviceCard key={d.deviceId} device={d} />
        ))}
        {all.length === 1 && filter === "all" && (
          <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed p-8 text-center">
            <span className="inline-flex size-10 items-center justify-center border border-input text-foreground">
              <Plus className="size-5" aria-hidden />
            </span>
            <span className="font-semibold">Ajoutez un second téléphone</span>
            <span className="max-w-sm text-sm text-muted-foreground">
              Avec deux appareils, la passerelle bascule seule quand l'un tombe ou atteint son plafond.
            </span>
            <button className="btn-secondary mt-1" onClick={() => setEnrolling(true)}>
              Scanner un QR code
            </button>
          </div>
        )}
      </div>

      {enrolling && <EnrollDialog onClose={() => setEnrolling(false)} />}
    </>
  );
}

type Filter = "all" | "online" | "offline";

/** How much a phone needs looking at: off, then on but not ready, then fine; disabled last. */
function severity(d: Device): number {
  if (!d.enabled) return 0;
  if (!d.connected) return 3;
  if (!d.ready) return 2;
  return 1;
}

function DeviceCard({ device: d }: { device: Device }) {
  const queryClient = useQueryClient();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [editingRules, setEditingRules] = useState(false);
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["devices"] });
    queryClient.invalidateQueries({ queryKey: ["stats"] });
  };

  const toggle = useMutation({ mutationFn: (enabled: boolean) => api.setDeviceEnabled(d.deviceId, enabled), onSuccess: refresh });
  const remove = useMutation({
    mutationFn: () => api.deleteDevice(d.deviceId),
    onSuccess: () => {
      setConfirmDelete(false);
      refresh();
    },
  });

  const h = d.health;
  const state: { tone: Tone; label: string } = !d.enabled
    ? { tone: "neutral", label: "Désactivé" }
    : d.ready
      ? { tone: "success", label: "Prêt à envoyer" }
      : d.connected
        ? { tone: "warning", label: "Connecté, pas prêt" }
        : { tone: "neutral", label: "Hors ligne" };
  const limits = d.policy?.limits;
  const usedHour = d.usage?.lastHour ?? 0;

  return (
    <article className="card flex flex-col overflow-hidden" aria-label={`${d.label || "Sans nom"}, ${state.label.toLowerCase()}`}>
      {d.enabled && !d.connected && (
        <div className="flex items-center gap-2 border-b border-danger-line bg-danger-soft px-5 py-2 text-[13px]">
          <span className="size-1.5 bg-destructive" aria-hidden />
          <span className="font-medium text-destructive">
            Hors ligne{d.lastSeenAt ? ` depuis ${formatRelative(d.lastSeenAt).replace(/^il y a /, "")}` : ""}
          </span>
          <span className="text-muted-foreground">· l'envoi passe par les autres téléphones</span>
        </div>
      )}
      <div className="flex items-start gap-3 p-5 pb-4">
        <div
          className={cn(
            "flex size-11 shrink-0 items-center justify-center rounded-md",
            state.tone === "success"
              ? "bg-success-soft text-success"
              : state.tone === "warning"
                ? "bg-warning-soft text-warning"
                : "bg-muted text-muted-foreground",
          )}
        >
          <Smartphone className="size-5" aria-hidden />
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="truncate font-semibold">{d.label || "Sans nom"}</h2>
          <p className="truncate text-sm text-muted-foreground">
            {[d.model, d.osVersion && `Android ${d.osVersion}`, d.carrier].filter(Boolean).join(" · ") || shortId(d.deviceId)}
          </p>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1.5">
          <Pill tone={state.tone}>{state.label}</Pill>
          {d.enabled && !d.availableNow && <Pill tone="warning">Hors horaires</Pill>}
          {d.enabled && atCap(d) && <Pill tone="warning">Plafond atteint</Pill>}
        </div>
      </div>

      {!d.enabled && (
        <div className="mx-5 mb-4 flex items-center gap-3 rounded-md border border-dashed bg-muted/40 px-3 py-2 text-sm">
          <span className="flex-1 text-muted-foreground">En pause : ne reçoit aucun envoi, et revient seul dès sa réactivation.</span>
          <button className="btn-primary h-8 px-3" onClick={() => toggle.mutate(true)} disabled={toggle.isPending}>
            Réactiver
          </button>
        </div>
      )}

      {h ? (
        <div className="grid grid-cols-3 border-y">
          <Metric icon={h.isCharging ? <BatteryCharging aria-hidden /> : <BatteryMedium aria-hidden />} label={h.isCharging ? "En charge" : "Batterie"}>
            {h.batteryLevel} %
            <Meter
              value={h.batteryLevel / 100}
              tone={h.isCharging || h.batteryLevel >= 40 ? "success" : h.batteryLevel >= 20 ? "warning" : "danger"}
            />
          </Metric>
          <Metric icon={<Signal aria-hidden />} label={h.networkType ? `Signal · ${h.networkType}` : "Signal"}>
            <span className="inline-flex items-center gap-2">
              {h.signalStrength}/4 <SignalBars level={h.signalStrength} />
            </span>
          </Metric>
          <Metric icon={<Send aria-hidden />} label="Envoyés · 1 h">
            <span className={cn(atCap(d) && "text-warning")}>
              {usedHour}
              {limits?.perHour ? <span className="text-sm font-normal text-muted-foreground"> / {limits.perHour}</span> : null}
            </span>
            {limits?.perHour ? (
              <Meter value={usedHour / limits.perHour} tone={usedHour >= limits.perHour ? "warning" : "primary"} />
            ) : null}
          </Metric>
        </div>
      ) : (
        <p className="border-y px-5 py-4 text-sm text-muted-foreground">Aucun rapport de santé : ce téléphone ne s'est jamais connecté.</p>
      )}

      <div className="flex flex-1 flex-col gap-3 p-5">
        {h && !h.permissionsOk && (
          <div className="rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-sm text-destructive">
            Permission SMS manquante sur le téléphone : il ne peut rien envoyer.
          </div>
        )}

        {d.sims && d.sims.length > 0 && (
          <div className="overflow-hidden rounded-md border">
            {d.sims.map((sim) => {
              const used = d.usage?.sims?.[String(sim.subscriptionId)]?.lastHour ?? 0;
              const simPolicy = d.policy?.sims?.find((x) => x.subscriptionId === sim.subscriptionId);
              const cap = simPolicy?.limits?.perHour;
              return (
                <div key={sim.subscriptionId} className="flex items-center gap-3 border-b px-3 py-2.5 text-sm last:border-0">
                  <span className="rounded-sm bg-muted px-1.5 py-0.5 text-xs font-medium">SIM {sim.slotIndex + 1}</span>
                  <span className="min-w-0 flex-1 truncate">
                    {sim.carrier || sim.displayName || "Opérateur inconnu"}
                    {sim.phoneNumber && <span className="ml-2 font-mono text-muted-foreground">{sim.phoneNumber}</span>}
                    {sim.isDefaultSms && <span className="ml-2 font-mono text-[11px] text-muted-foreground uppercase">par défaut</span>}
                    {simPolicy?.disabled && <span className="ml-2 text-xs text-muted-foreground">désactivée</span>}
                  </span>
                  <SignalBars level={sim.signalStrength} />
                  <span className="w-20 text-right text-xs text-muted-foreground tabular-nums">
                    {used}
                    {cap ? `/${cap}` : ""} · 1 h
                  </span>
                  <span
                    className={cn("size-2", sim.simReady ? "bg-success" : "bg-muted-foreground/30")}
                    aria-label={sim.simReady ? "SIM prête" : "SIM indisponible"}
                  />
                </div>
              );
            })}
          </div>
        )}

        <button
          type="button"
          onClick={() => setEditingRules(true)}
          className="flex w-full items-center gap-2 rounded-md border border-dashed px-3 py-2 text-left text-xs text-muted-foreground transition-colors hover:border-solid hover:bg-row-hover"
        >
          <SlidersHorizontal className="size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1 truncate">{rulesSummary(d)}</span>
          <span className="shrink-0 font-medium text-primary">Modifier</span>
        </button>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 border-t bg-muted/30 px-5 py-3">
        <span className="text-xs text-muted-foreground" title={formatDateTime(d.lastSeenAt)}>
          Vu {formatRelative(d.lastSeenAt)}
        </span>
        <div className="flex gap-1">
          <Link to={`/history?deviceId=${d.deviceId}`} className="btn-ghost">
            <MessageSquare className="size-4" aria-hidden />
            Messages
          </Link>
          {d.enabled && (
            <button className="btn-ghost" onClick={() => toggle.mutate(false)} disabled={toggle.isPending}>
              <Power className="size-4" aria-hidden />
              Désactiver
            </button>
          )}
          <button
            className="btn-ghost text-destructive hover:text-destructive"
            onClick={() => setConfirmDelete(true)}
            aria-label="Retirer"
            title="Retirer"
          >
            <Trash2 className="size-4" aria-hidden />
          </button>
        </div>
      </div>
      {toggle.error && (
        <div className="px-5 pb-3">
          <ErrorNote error={toggle.error} />
        </div>
      )}

      {editingRules && <PolicyDialog device={d} onClose={() => setEditingRules(false)} />}

      {confirmDelete && (
        <ConfirmDialog
          title={`Retirer « ${d.label || shortId(d.deviceId)} » ?`}
          body={
            <>
              Le téléphone perd son accès immédiatement et devra être ré-enrôlé avec un nouveau QR code. Son historique d'envoi est
              conservé. Pour une pause, préférez « Désactiver ».
              <div className="mt-3">
                <ErrorNote error={remove.error} />
              </div>
            </>
          }
          confirmLabel="Retirer"
          busy={remove.isPending}
          onConfirm={() => remove.mutate()}
          onCancel={() => setConfirmDelete(false)}
        />
      )}
    </article>
  );
}

function Metric({ icon, label, children }: { icon: ReactNode; label: string; children: ReactNode }) {
  return (
    <div className="border-r px-5 py-3 last:border-r-0">
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground [&_svg]:size-3.5">
        {icon}
        <span className="truncate">{label}</span>
      </div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{children}</div>
    </div>
  );
}

/** A thin bar under a figure: how full it is against its maximum. */
function Meter({ value, tone }: { value: number; tone: "success" | "warning" | "danger" | "primary" }) {
  const bg = { success: "bg-success", warning: "bg-warning", danger: "bg-destructive", primary: "bg-primary" }[tone];
  return (
    <div className="mt-1.5 h-1 overflow-hidden bg-muted" aria-hidden>
      <div className={cn("h-full", bg)} style={{ width: `${Math.min(100, Math.max(0, value * 100))}%` }} />
    </div>
  );
}

/**
 * An enrollment code minted on mount — a fresh, single-use token each time,
 * never a cached one — with its countdown. onEnrolled runs when a phone joins:
 * the code is spent either way.
 */
function useEnrollment(onEnrolled: () => void) {
  const queryClient = useQueryClient();
  const mint = useMutation({ mutationFn: api.enrollToken });
  const [now, setNow] = useState(Date.now());

  const { mutate } = mint;
  useEffect(() => mutate(), [mutate]);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  useLiveEvent((e) => {
    if (e.type === "device.enrolled") {
      queryClient.invalidateQueries({ queryKey: ["devices"] });
      onEnrolled();
    }
  });

  const expiresIn = mint.data ? Math.max(0, Math.floor((new Date(mint.data.expiresAt).getTime() - now) / 1000)) : 0;
  const expired = mint.data !== undefined && expiresIn === 0;
  return { mint, expiresIn, expired };
}

/** The QR code to scan, its countdown, and the payload for a phone without a camera. */
function EnrollCode({ enrollment: { mint, expiresIn, expired } }: { enrollment: ReturnType<typeof useEnrollment> }) {
  return (
    <div className="flex flex-col items-center">
      {mint.isPending && (
        <div className="flex size-64 items-center justify-center rounded-md border border-dashed text-sm text-muted-foreground">
          <Loader2 className="mr-2 size-4 animate-spin" aria-hidden />
          Génération…
        </div>
      )}
      {mint.data && (
        <img
          src={`data:image/png;base64,${mint.data.qrCodePngBase64}`}
          alt="QR code d'enrôlement"
          // White behind the code in both themes: a scanner wants dark-on-light.
          className={cn("size-64 rounded-md border bg-white p-2", expired && "opacity-20")}
        />
      )}
      <ErrorNote error={mint.error} />
      {mint.data && (
        <p className="mt-3 text-sm text-muted-foreground">
          {expired ? "Ce code a expiré." : `Usage unique · expire dans ${Math.floor(expiresIn / 60)} min ${String(expiresIn % 60).padStart(2, "0")} s`}
        </p>
      )}
      {mint.data && !expired && <CopyPayload payload={mint.data.payload} />}
    </div>
  );
}

function EnrollDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const close = () => {
    // The phone may have joined while the drawer was open.
    queryClient.invalidateQueries({ queryKey: ["devices"] });
    onClose();
  };
  const enrollment = useEnrollment(close);

  return (
    <Modal onClose={close} label="Ajouter un téléphone" description="Le téléphone apparaît ici dès qu'il se connecte.">
      <EnrollSteps />
      <div className="mt-6">
        <EnrollCode enrollment={enrollment} />
      </div>

      <ModalActions>
        <button className="btn-secondary" onClick={() => enrollment.mint.mutate()} disabled={enrollment.mint.isPending}>
          <RefreshCw className="size-4" aria-hidden />
          Nouveau code
        </button>
        <button className="btn-primary" onClick={close}>
          Terminé
        </button>
      </ModalActions>
    </Modal>
  );
}

function EnrollSteps() {
  const steps: [string, string][] = [
    ["Installer l'application Relaix sur le téléphone", "Android 8 ou plus, avec une SIM locale."],
    ["Dans l'app, scanner ce code", "Pas de caméra ? Copiez la configuration et collez-la dans l'app."],
    ["Autoriser l'envoi de SMS et garder l'app active", "Désactivez l'optimisation de batterie pour Relaix."],
  ];
  return (
    <ol className="flex flex-col gap-3.5">
      {steps.map(([title, hint], i) => (
        <li key={title} className="flex gap-3">
          <span className="inline-flex size-7 shrink-0 items-center justify-center border font-mono text-xs font-medium text-muted-foreground">
            {i + 1}
          </span>
          <span className="flex flex-col">
            <span className="text-sm font-medium">{title}</span>
            <span className="text-xs text-muted-foreground">{hint}</span>
          </span>
        </li>
      ))}
    </ol>
  );
}

/** No phone yet: the steps and the code on the page itself, waiting for the first. */
function Onboarding() {
  const enrollment = useEnrollment(() => {});
  return (
    <section aria-labelledby="ob-title" className="card flex flex-wrap items-center gap-8 p-8">
      <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-6">
        <div className="flex flex-col gap-1.5">
          <span className="eyebrow text-foreground">Première connexion</span>
          <h2 id="ob-title" className="text-xl font-semibold tracking-tight">
            Connectez votre premier téléphone
          </h2>
          <p className="text-sm text-muted-foreground">
            Un téléphone Android avec une SIM locale suffit. Il enverra les SMS de vos applications par sa propre ligne.
          </p>
        </div>
        <EnrollSteps />
        <div className="flex items-center gap-2.5 text-sm">
          <span className="relative flex size-2.5">
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary opacity-60 motion-reduce:hidden" />
            <span className="relative inline-flex size-2.5 bg-primary" />
          </span>
          <span className="font-medium">En attente du téléphone…</span>
          <span className="text-muted-foreground">il apparaîtra ici automatiquement</span>
        </div>
      </div>
      <div className="flex flex-[0_1_300px] flex-col items-center gap-2">
        <EnrollCode enrollment={enrollment} />
        {enrollment.expired && (
          <button className="btn-secondary mt-2" onClick={() => enrollment.mint.mutate()}>
            <RefreshCw className="size-4" aria-hidden />
            Nouveau code
          </button>
        )}
      </div>
    </section>
  );
}

/**
 * The QR's content as text, for a phone whose camera cannot see the screen —
 * the same screen it is plugged into, a remote desktop — where pasting it in
 * the agent's manual entry is the only way through.
 */
function CopyPayload({ payload }: { payload: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  return (
    <button
      type="button"
      className="mt-2 inline-flex items-center gap-1.5 text-sm font-medium underline decoration-foreground/30 underline-offset-4 hover:decoration-foreground"
      onClick={() =>
        // Unavailable off a secure origin (plain http to a LAN address): the QR still works.
        navigator.clipboard?.writeText(payload).then(() => setCopied(true), () => {})
      }
    >
      {copied ? <Check className="size-4" aria-hidden /> : <Copy className="size-4" aria-hidden />}
      {copied ? "Copié" : "Copier la configuration (saisie manuelle)"}
    </button>
  );
}

const DAY_SHORT = ["", "lun", "mar", "mer", "jeu", "ven", "sam", "dim"];
const hhmm = (m: number) => `${String(Math.floor(m / 60) % 24).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;

/** The rules in one line, so a card says what a phone takes without opening it. */
function rulesSummary(d: Device): string {
  const p = d.policy;
  const parts: string[] = [];
  const windows = p?.availability?.windows ?? [];
  if (windows.length) {
    parts.push(
      windows
        .map((w) => {
          const days = w.days?.length ? w.days.map((n) => DAY_SHORT[n]).join(" ") : "tous les jours";
          return `${days} ${hhmm(w.start)}–${hhmm(w.end)}`;
        })
        .join(", "),
    );
  }
  if (p?.accept?.countries?.length) parts.push(p.accept.countries.join(", "));
  if (p?.accept?.pattern) parts.push(`motif ${p.accept.pattern}`);
  if (p?.limits?.perHour) parts.push(`≤ ${p.limits.perHour}/h`);
  if (p?.limits?.perDay) parts.push(`≤ ${p.limits.perDay}/j`);
  const offSims = (p?.sims ?? []).filter((s) => s.disabled).length;
  const ruledSims = (p?.sims ?? []).length - offSims;
  if (offSims) parts.push(`${offSims} SIM désactivée${offSims > 1 ? "s" : ""}`);
  if (ruledSims) parts.push(`règles sur ${ruledSims} SIM`);
  return parts.length ? parts.join(" · ") : "Aucune règle : tous les numéros, à toute heure";
}

/** The device has used up one of its own caps: it takes no job until the window slides. */
function atCap(d: Device): boolean {
  const l = d.policy?.limits;
  const u = d.usage;
  if (!l || !u) return false;
  return (!!l.perHour && u.lastHour >= l.perHour) || (!!l.perDay && u.lastDay >= l.perDay);
}
