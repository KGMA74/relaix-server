/**
 * Behind the sign-in form: phones as stations on a grid, and messages hopping
 * between them along ruled lines — what the gateway does, drawn quietly. Pure
 * SVG and CSS; still for whoever asked for reduced motion.
 */
const NODES: [number, number][] = [
  [80, 120],
  [260, 60],
  [420, 180],
  [620, 90],
  [780, 220],
  [150, 330],
  [360, 380],
  [560, 330],
  [720, 440],
  [240, 520],
  [480, 560],
  [860, 360],
];

const LINKS: [number, number][] = [
  [0, 1],
  [1, 2],
  [2, 3],
  [3, 4],
  [0, 5],
  [5, 6],
  [2, 6],
  [6, 7],
  [7, 4],
  [7, 8],
  [5, 9],
  [9, 10],
  [6, 10],
  [8, 11],
  [4, 11],
];

export function RelayBackdrop() {
  return (
    <svg
      aria-hidden
      className="pointer-events-none absolute inset-0 h-full w-full text-foreground"
      viewBox="0 0 940 640"
      preserveAspectRatio="xMidYMid slice"
    >
      <defs>
        <pattern id="relay-grid" width="40" height="40" patternUnits="userSpaceOnUse">
          <path d="M40 0H0V40" fill="none" stroke="currentColor" strokeOpacity="0.05" />
        </pattern>
      </defs>
      <rect width="940" height="640" fill="url(#relay-grid)" />
      {LINKS.map(([a, b], i) => {
        const [x1, y1] = NODES[a]!;
        const [x2, y2] = NODES[b]!;
        return (
          <g key={i}>
            <line x1={x1} y1={y1} x2={x2} y2={y2} stroke="currentColor" strokeOpacity="0.1" />
            {/* A message travelling the link: a short dash sliding along it. */}
            <line
              x1={x1}
              y1={y1}
              x2={x2}
              y2={y2}
              stroke="currentColor"
              strokeOpacity="0.55"
              strokeWidth="1.5"
              strokeDasharray="10 400"
              className="relay-hop"
              style={{ animationDelay: `${(i * 0.7) % 5}s`, animationDuration: `${4 + (i % 4)}s` }}
            />
          </g>
        );
      })}
      {NODES.map(([x, y], i) => (
        <g key={i}>
          <rect x={x - 4} y={y - 4} width="8" height="8" fill="var(--background)" stroke="currentColor" strokeOpacity="0.35" />
          <rect x={x - 4} y={y - 4} width="8" height="8" fill="currentColor" className="relay-blink" style={{ animationDelay: `${(i * 1.3) % 6}s` }} />
        </g>
      ))}
    </svg>
  );
}
