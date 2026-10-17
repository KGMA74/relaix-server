import { AlertCircle, X } from "lucide-react";
import type { ReactNode } from "react";
import { ApiError } from "../api/client";
import type { JobStatus } from "../api/types";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";

export const STATUS_LABEL: Record<JobStatus, string> = {
  pending: "En attente",
  assigned: "Assigné",
  sent: "Envoyé",
  delivered: "Délivré",
  failed: "Échoué",
  cancelled: "Annulé",
};

export type Tone = "success" | "warning" | "danger" | "info" | "primary" | "neutral";

/** What each status means, rather than which colour it happens to be. */
export const STATUS_TONE: Record<JobStatus, Tone> = {
  pending: "warning",
  assigned: "info",
  sent: "primary",
  delivered: "success",
  failed: "danger",
  cancelled: "neutral",
};

const TONE_PILL: Record<Tone, string> = {
  success: "bg-success-soft text-success border-success-line",
  warning: "bg-warning-soft text-warning border-warning-line",
  danger: "bg-danger-soft text-destructive border-danger-line",
  info: "bg-info-soft text-info border-info-line",
  primary: "bg-card text-foreground border-input",
  neutral: "bg-muted text-muted-foreground border-border",
};

const TONE_DOT: Record<Tone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-destructive",
  info: "bg-info",
  primary: "bg-primary",
  neutral: "bg-muted-foreground/50",
};

export function Pill({ tone, children, className }: { tone: Tone; children: ReactNode; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-px font-mono text-[11px] font-medium tracking-wide whitespace-nowrap uppercase",
        TONE_PILL[tone],
        className,
      )}
    >
      <span className={cn("size-1.5", TONE_DOT[tone])} aria-hidden />
      {children}
    </span>
  );
}

export function StatusBadge({ status }: { status: JobStatus }) {
  return <Pill tone={STATUS_TONE[status]}>{STATUS_LABEL[status]}</Pill>;
}

export function Dot({ on, label }: { on: boolean; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
      <span className={cn("size-2", on ? "bg-success" : "bg-muted-foreground/30")} />
      {label}
    </span>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
  eyebrow,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
  /** A word above the title that says which part of the console this is. */
  eyebrow?: string;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4 border-b pb-5">
      <div className="min-w-0">
        {eyebrow && <p className="eyebrow mb-1.5">{eyebrow}</p>}
        <h1 className="text-title font-semibold">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-muted-foreground">{subtitle}</p>}
      </div>
      {/* On a narrow screen the actions wrap instead of pushing the title out. */}
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

/** The header row of a card: a title on the left, an action or a link on the right. */
export function CardHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b px-4 py-3">
      <div className="min-w-0">
        <h2 className="eyebrow text-foreground">{title}</h2>
        {description && <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>}
      </div>
      {actions}
    </div>
  );
}

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null;
  const message =
    error instanceof ApiError ? `${error.message} (${error.code})` : error instanceof Error ? error.message : String(error);
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-sm text-destructive"
    >
      <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span className="min-w-0 break-words">{message}</span>
    </div>
  );
}

export function Empty({ children, icon }: { children: ReactNode; icon?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-12 text-center text-sm text-muted-foreground">
      {icon && <div className="text-muted-foreground/60 [&_svg]:size-8">{icon}</div>}
      <div>{children}</div>
    </div>
  );
}

/** Signal bars for the normalized 0–4 level the agents report. */
export function SignalBars({ level }: { level: number }) {
  const tone = level <= 0 ? "bg-destructive" : level === 1 ? "bg-warning" : "bg-foreground/80";
  return (
    <span className="inline-flex items-end gap-0.5" title={`Signal ${level}/4`} aria-label={`Signal ${level} sur 4`}>
      {[1, 2, 3, 4].map((i) => (
        <span
          key={i}
          className={cn("w-1", i <= level ? tone : "bg-muted-foreground/25")}
          style={{ height: 3 + i * 3 }}
        />
      ))}
    </span>
  );
}

/** In-app confirmation, so a destructive action never relies on window.confirm. */
export function ConfirmDialog({
  title,
  body,
  confirmLabel,
  busy,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onCancel()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription asChild>
            <div>{body}</div>
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel} disabled={busy}>
            Annuler
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={busy}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * A drawer from the right, for forms and details: the page stays in view
 * behind it. `label` is its title, `description` a line under it; the content
 * scrolls between that header and the ModalActions, which stay in reach.
 * Confirmations keep the centered ConfirmDialog.
 */
export function Modal({
  children,
  onClose,
  wide,
  label = "Détails",
  description,
}: {
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
  label?: ReactNode;
  description?: ReactNode;
}) {
  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        side="right"
        showCloseButton={false}
        {...(description ? {} : { "aria-describedby": undefined })}
        className={cn(
          "gap-0 bg-popover shadow-drawer",
          "w-full sm:max-w-none",
          wide ? "sm:w-[640px]" : "sm:w-[440px]",
        )}
      >
        <header className="flex items-start gap-3 border-b px-5 py-4">
          <div className="min-w-0 flex-1">
            <SheetTitle className="text-base font-semibold tracking-tight">{label}</SheetTitle>
            {description && <SheetDescription className="mt-0.5 text-sm text-muted-foreground">{description}</SheetDescription>}
          </div>
          <SheetClose
            aria-label="Fermer"
            className="inline-flex size-8 shrink-0 items-center justify-center rounded-md border text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            <X className="size-4" aria-hidden />
          </SheetClose>
        </header>
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-5 pt-5 pb-5">{children}</div>
      </SheetContent>
    </Sheet>
  );
}

/** A drawer's buttons, kept at its foot while the content scrolls. */
export function ModalActions({ children }: { children: ReactNode }) {
  return (
    <div className="sticky bottom-0 -mx-5 -mb-5 mt-auto flex flex-wrap justify-end gap-2 border-t bg-popover px-5 py-3 pt-3">
      {children}
    </div>
  );
}
