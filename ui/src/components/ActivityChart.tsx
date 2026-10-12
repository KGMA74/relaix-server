import { BarChart3, Table2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ActivityBucket } from "../api/types";
import { cn } from "@/lib/utils";

/**
 * Messages per bucket, stacked by outcome.
 *
 * Built to the dataviz method: status hues (validated per theme, see
 * index.css), bars capped at 24px with a 4px rounded top and a square base,
 * a 2px surface gap between stacked segments, a hairline grid, a legend
 * always present, a per-column tooltip, and a table view so no figure is
 * reachable by colour alone.
 */

const SERIES = [
  { key: "delivered", label: "Envoyés", color: "var(--chart-ok)" },
  { key: "inFlight", label: "En cours", color: "var(--chart-flight)" },
  { key: "failed", label: "Échoués", color: "var(--chart-fail)" },
] as const;

type SeriesKey = (typeof SERIES)[number]["key"];

const PLOT_H = 200;
const AXIS_H = 26;
const LEFT = 36;
const GAP = 2;
const MAX_BAR = 24;
const RADIUS = 4;

/**
 * A tick step of 1, 2 or 5 × 10^n, so four steps reach at least v. Counts are
 * whole messages: a "2.5" tick would describe a message that cannot exist.
 */
function niceStep(v: number): number {
  const raw = Math.max(1, v / 4);
  const mag = 10 ** Math.floor(Math.log10(raw));
  for (const m of [1, 2, 5, 10]) {
    if (m * mag >= raw) return Math.max(1, m * mag);
  }
  return 10 * mag;
}

const compact = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}K` : String(n));

function bucketLabel(iso: string, bucketSeconds: number, long = false): string {
  const d = new Date(iso);
  if (bucketSeconds < 86_400) {
    const time = d.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
    return long || bucketSeconds >= 6 * 3600
      ? `${d.toLocaleDateString("fr-FR", { day: "numeric", month: "short" })} ${time}`
      : time;
  }
  return d.toLocaleDateString("fr-FR", { day: "numeric", month: "short" });
}

/** A rect with only its top corners rounded: the data end, never the baseline. */
function topRounded(x: number, y: number, w: number, h: number, r: number): string {
  const rr = Math.min(r, h, w / 2);
  return `M${x},${y + h} V${y + rr} Q${x},${y} ${x + rr},${y} H${x + w - rr} Q${x + w},${y} ${x + w},${y + rr} V${y + h} Z`;
}

export function ActivityChart({ buckets, bucketSeconds }: { buckets: ActivityBucket[]; bucketSeconds: number }) {
  const wrap = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(600);
  const [hover, setHover] = useState<number | null>(null);
  const [asTable, setAsTable] = useState(false);

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => e && setWidth(Math.max(240, e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const totals = useMemo(() => {
    const t: Record<SeriesKey, number> = { delivered: 0, inFlight: 0, failed: 0 };
    for (const b of buckets) for (const s of SERIES) t[s.key] += b[s.key];
    return t;
  }, [buckets]);

  const step = niceStep(Math.max(0, ...buckets.map((b) => b.delivered + b.inFlight + b.failed)));
  const max = step * 4;
  const ticks = [0, step, 2 * step, 3 * step, max];
  const plotW = width - LEFT;
  const slot = buckets.length ? plotW / buckets.length : plotW;
  const barW = Math.max(2, Math.min(MAX_BAR, slot * 0.62));
  const y = (v: number) => PLOT_H - (v / max) * PLOT_H;
  const labelEvery = Math.max(1, Math.ceil(buckets.length / Math.max(2, Math.floor(plotW / 70))));
  const empty = buckets.every((b) => b.delivered + b.inFlight + b.failed === 0);

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-2">
        {/* Legend: always present, values in text ink beside a coloured key. */}
        <ul className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs" aria-label="Légende">
          {SERIES.map((s) => (
            <li key={s.key} className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-[3px]" style={{ background: s.color }} aria-hidden />
              <span className="text-muted-foreground">{s.label}</span>
              <span className="font-medium tabular-nums">{totals[s.key]}</span>
            </li>
          ))}
        </ul>
        <button
          type="button"
          onClick={() => setAsTable((v) => !v)}
          className="ml-auto inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          aria-pressed={asTable}
        >
          {asTable ? <BarChart3 className="size-3.5" aria-hidden /> : <Table2 className="size-3.5" aria-hidden />}
          {asTable ? "Graphique" : "Tableau"}
        </button>
      </div>

      {asTable ? (
        <div className="max-h-[226px] overflow-auto rounded-md border">
          <table className="w-full text-sm">
            <thead className="sticky top-0 bg-card">
              <tr className="border-b">
                <th className="th">Période</th>
                {SERIES.map((s) => (
                  <th key={s.key} className="th text-right">
                    {s.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y">
              {[...buckets].reverse().map((b) => (
                <tr key={b.start}>
                  <td className="td py-1.5 text-muted-foreground">{bucketLabel(b.start, bucketSeconds, true)}</td>
                  {SERIES.map((s) => (
                    <td key={s.key} className="td py-1.5 text-right tabular-nums">
                      {b[s.key] || "—"}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div ref={wrap} className="relative" onMouseLeave={() => setHover(null)}>
          <svg width={width} height={PLOT_H + AXIS_H} role="img" aria-label="Messages par période et par issue" className="block overflow-visible">
            {/* Recessive hairline grid, solid, with compact round ticks. */}
            {ticks.map((t) => (
              <g key={t}>
                <line x1={LEFT} x2={width} y1={y(t)} y2={y(t)} stroke="var(--chart-grid)" strokeWidth={1} />
                <text x={LEFT - 8} y={y(t)} dy="0.32em" textAnchor="end" className="fill-muted-foreground text-[11px] tabular-nums">
                  {compact(t)}
                </text>
              </g>
            ))}

            {buckets.map((b, i) => {
              const cx = LEFT + slot * i + slot / 2;
              const x = cx - barW / 2;
              const segs = SERIES.map((s) => ({ ...s, v: b[s.key] })).filter((s) => s.v > 0);
              let base = 0;
              const topIndex = segs.length - 1;
              return (
                <g key={b.start}>
                  {hover === i && (
                    <rect x={LEFT + slot * i} y={0} width={slot} height={PLOT_H} className="fill-muted" opacity={0.6} rx={4} />
                  )}
                  {segs.map((s, j) => {
                    const y0 = y(base);
                    const y1 = y(base + s.v);
                    base += s.v;
                    // The gap is cut from the segment's bottom, so the stack
                    // keeps its true height and the base stays square.
                    const h = Math.max(0, y0 - y1 - (j > 0 ? GAP : 0));
                    if (h <= 0) return null;
                    return j === topIndex ? (
                      <path key={s.key} d={topRounded(x, y1, barW, h, RADIUS)} fill={s.color} />
                    ) : (
                      <rect key={s.key} x={x} y={y1} width={barW} height={h} fill={s.color} />
                    );
                  })}
                  {i % labelEvery === 0 && (
                    <text x={cx} y={PLOT_H + 17} textAnchor="middle" className="fill-muted-foreground text-[11px]">
                      {bucketLabel(b.start, bucketSeconds)}
                    </text>
                  )}
                  {/* Hit target: the whole slot, far larger than the bar. */}
                  <rect
                    x={LEFT + slot * i}
                    y={0}
                    width={slot}
                    height={PLOT_H}
                    fill="transparent"
                    onMouseEnter={() => setHover(i)}
                    onFocus={() => setHover(i)}
                    tabIndex={-1}
                  />
                </g>
              );
            })}
            <line x1={LEFT} x2={width} y1={PLOT_H} y2={PLOT_H} stroke="var(--chart-grid)" strokeWidth={1} />
          </svg>

          {empty && (
            <div className="pointer-events-none absolute inset-x-0 top-0 flex h-[200px] items-center justify-center pl-9 text-sm text-muted-foreground">
              Aucun message sur la période.
            </div>
          )}

          {hover !== null && buckets[hover] && (
            <Tooltip
              bucket={buckets[hover]}
              bucketSeconds={bucketSeconds}
              left={Math.min(Math.max(LEFT + slot * hover + slot / 2, 90), width - 90)}
            />
          )}
        </div>
      )}
    </div>
  );
}

function Tooltip({ bucket, bucketSeconds, left }: { bucket: ActivityBucket; bucketSeconds: number; left: number }) {
  const total = bucket.delivered + bucket.inFlight + bucket.failed;
  return (
    <div
      className="pointer-events-none absolute top-1 z-10 w-44 -translate-x-1/2 rounded-md border bg-popover p-2.5 text-xs shadow-surface"
      style={{ left }}
      role="status"
    >
      <div className="mb-1.5 font-medium">{bucketLabel(bucket.start, bucketSeconds, true)}</div>
      {SERIES.map((s) => (
        <div key={s.key} className="flex items-center gap-1.5 py-0.5">
          <span className="size-2 rounded-[2px]" style={{ background: s.color }} aria-hidden />
          <span className="text-muted-foreground">{s.label}</span>
          <span className={cn("ml-auto tabular-nums", bucket[s.key] ? "font-medium" : "text-muted-foreground")}>{bucket[s.key]}</span>
        </div>
      ))}
      <div className="mt-1.5 flex border-t pt-1.5">
        <span className="text-muted-foreground">Total</span>
        <span className="ml-auto font-medium tabular-nums">{total}</span>
      </div>
      {bucket.cancelled > 0 && <div className="mt-1 text-muted-foreground">+ {bucket.cancelled} annulé(s)</div>}
    </div>
  );
}
