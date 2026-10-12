import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BatteryCharging, BatteryMedium, Check, Copy, Loader2, MessageSquare, Plus, Power, RefreshCw, Send, Signal, SlidersHorizontal, Smartphone, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { Device } from "../api/types";
import { ConfirmDialog, Empty, ErrorNote, Modal, PageHeader, Pill, SignalBars, type Tone } from "../components/common";
import { PolicyDialog } from "../components/PolicyDialog";
import { formatDateTime, formatRelative, shortId } from "../lib/format";
import { useLiveEvent, usePollInterval } from "@/lib/live";
import { cn } from "@/lib/utils";

export function DevicesPage() {
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices, refetchInterval: usePollInterval(10_000) });
  const [enrolling, setEnrolling] = useState(false);
  const list = devices.data?.devices ?? [];

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

      {devices.isSuccess && list.length === 0 && (
        <div className="card">
          <Empty icon={<Smartphone />}>
            Aucun téléphone enrôlé. Installez l'agent Relaix sur un Android, puis scannez le QR code d'enrôlement.
          </Empty>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-2">
        {list.map((d) => (
          <DeviceCard key={d.deviceId} device={d} />
        ))}
      </div>

      {enrolling && <EnrollDialog onClose={() => setEnrolling(false)} />}
    </>
  );
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
    <div className="card flex flex-col overflow-hidden">
      <div className="flex items-start gap-3 p-5 pb-4">
        <div
          className={cn(
            "flex size-11 shrink-0 items-center justify-center rounded-lg",
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
                    {sim.isDefaultSms && <span className="ml-2 text-xs text-primary">par défaut</span>}
                    {simPolicy?.disabled && <span className="ml-2 text-xs text-muted-foreground">désactivée</span>}
                  </span>
                  <SignalBars level={sim.signalStrength} />
                  <span className="w-20 text-right text-xs text-muted-foreground tabular-nums">
                    {used}
                    {cap ? `/${cap}` : ""} · 1 h
                  </span>
                  <span
                    className={cn("size-2 rounded-full", sim.simReady ? "bg-success" : "bg-muted-foreground/30")}
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
    </div>
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
    <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-muted" aria-hidden>
      <div className={cn("h-full rounded-full", bg)} style={{ width: `${Math.min(100, Math.max(0, value * 100))}%` }} />
    </div>
  );
}

function EnrollDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const mint = useMutation({ mutationFn: api.enrollToken });
  const [now, setNow] = useState(Date.now());

  // Mint once on open; a fresh token per opening, never a cached one.
  const { mutate } = mint;
  useEffect(() => mutate(), [mutate]);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  const close = () => {
    // The phone may have joined while the dialog was open.
    queryClient.invalidateQueries({ queryKey: ["devices"] });
    onClose();
  };

  // The phone scanned the code: nothing left to do here. Any enrollment
  // closes it — the token is single-use, so the one this dialog shows is
  // spent either way.
  useLiveEvent((e) => {
    if (e.type === "device.enrolled") close();
  });

  const expiresIn = mint.data ? Math.max(0, Math.floor((new Date(mint.data.expiresAt).getTime() - now) / 1000)) : 0;
  const expired = mint.data !== undefined && expiresIn === 0;

  return (
    <Modal onClose={close} label="Ajouter un téléphone">
      <h2 className="text-lg font-semibold">Ajouter un téléphone</h2>
      <ol className="mt-2 list-decimal space-y-1 pl-5 text-sm text-muted-foreground">
        <li>Installez l'agent Relaix sur le téléphone Android.</li>
        <li>Ouvrez-le et scannez ce QR code.</li>
        <li>Le téléphone apparaît ici dès qu'il se connecte.</li>
      </ol>

      <div className="mt-5 flex flex-col items-center">
        {mint.isPending && (
          <div className="flex size-64 items-center justify-center rounded-lg border border-dashed text-sm text-muted-foreground">
            <Loader2 className="mr-2 size-4 animate-spin" aria-hidden />
            Génération…
          </div>
        )}
        {mint.data && (
          <img
            src={`data:image/png;base64,${mint.data.qrCodePngBase64}`}
            alt="QR code d'enrôlement"
            // White behind the code in both themes: a scanner wants dark-on-light.
            className={cn("size-64 rounded-lg border bg-white p-2", expired && "opacity-20")}
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

      <div className="mt-6 flex justify-end gap-2">
        <button className="btn-secondary" onClick={() => mint.mutate()} disabled={mint.isPending}>
          <RefreshCw className="size-4" aria-hidden />
          Nouveau code
        </button>
        <button className="btn-primary" onClick={close}>
          Terminé
        </button>
      </div>
    </Modal>
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
      className="mt-2 inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
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
