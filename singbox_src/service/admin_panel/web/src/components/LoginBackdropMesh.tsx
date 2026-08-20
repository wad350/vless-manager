import { Box } from "@mui/material";
import { useEffect, useMemo, useRef, useState } from "react";

// LoginBackdropMesh renders a stable network mesh: a fixed set of
// nodes connected by edges that are always faintly visible. Every
// few seconds one or two independent "traces" start somewhere in
// the mesh and travel hop-by-hop along their own random walk
// through the graph — each edge in a trace lights up in turn,
// followed by the destination node briefly pulsing as the trace
// "arrives" there.
//
// Each trace looks like a single data flow finding its way across
// multiple hops; concurrent traces look like several independent
// flows sharing the same network.
//
// Implementation notes:
//   - Each trace is computed at spawn time as a sequence of edge
//     hops (a random walk that avoids revisiting nodes). The
//     sequence is rendered as overlay <line>/<circle> elements
//     with per-element CSS `animation-delay`, so the browser
//     handles the timing — no per-frame JS work after mount.
//   - Multiple traces can be in flight at the same time. The
//     spawner produces 1–2 traces per cycle and waits ~1.5–2.5 s
//     between cycles, so the mesh always feels alive but never
//     drowns the form behind it.
//   - The accent colour comes from `var(--sb-accent)` so the mesh
//     instantly retints when the user picks a new accent.
//   - `prefers-reduced-motion` keeps the static mesh visible but
//     suppresses the trace spawner.

interface Node {
  x: number;
  y: number;
  r: number;
}

interface Edge {
  a: number;
  b: number;
}

// One hop of a trace: the edge being traversed, the node the
// trace lands on at the end of the hop, and how many milliseconds
// after the trace's start that hop fires.
interface TraceStep {
  edgeIdx: number;
  fromNodeIdx: number;
  toNodeIdx: number;
  delay: number;
}

interface Trace {
  id: number;
  originIdx: number;
  steps: TraceStep[];
  totalMs: number;
}

const VB_W = 1600;
const VB_H = 1000;
const EDGE_FIRE_MS = 600;
const NODE_FIRE_MS = 700;
// Time between successive hops. The actual edge highlight lasts
// EDGE_FIRE_MS, so stepping a touch faster than that produces
// overlap between adjacent hops — the trace reads as a continuous
// flow rather than a discrete dot-dot-dot.
const STEP_MS = 480;

// Build a stable mesh: a 7×5 grid of nodes (35 total) with per-cell
// jitter, connected by an edge wherever two nodes are nearer than
// ~1 cell diagonal. The grid is intentionally laid out across an
// area larger than the viewBox on each axis (`OVERSHOOT`) so the
// outermost nodes sit just past the visible edge of the page —
// traces can start in view and visibly continue off-screen, which
// gives the impression that the network extends beyond the form
// rather than being neatly framed by it.
const OVERSHOOT_X = 130;
const OVERSHOOT_Y = 90;
function buildGraph(): { nodes: Node[]; edges: Edge[] } {
  const cols = 7;
  const rows = 5;
  const startX = -OVERSHOOT_X;
  const startY = -OVERSHOOT_Y;
  const spanX = VB_W + OVERSHOOT_X * 2;
  const spanY = VB_H + OVERSHOOT_Y * 2;
  const cellW = spanX / cols;
  const cellH = spanY / rows;
  const nodes: Node[] = [];
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      nodes.push({
        x: startX + cellW * (c + 0.5) + (Math.random() - 0.5) * cellW * 0.5,
        y: startY + cellH * (r + 0.5) + (Math.random() - 0.5) * cellH * 0.5,
        r: 2.4 + Math.random() * 1.2,
      });
    }
  }
  const threshold = Math.hypot(cellW, cellH) * 1.0;
  const edges: Edge[] = [];
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      const a = nodes[i];
      const b = nodes[j];
      if (Math.hypot(a.x - b.x, a.y - b.y) < threshold) {
        edges.push({ a: i, b: j });
      }
    }
  }
  return { nodes, edges };
}

// Build adjacency: for every node, the list of (edgeIdx,
// neighbourIdx) pairs incident on it. Used by the random-walk
// trace builder to step from node to node along real edges.
function buildAdjacency(
  nodes: Node[],
  edges: Edge[],
): { edgeIdx: number; nodeIdx: number }[][] {
  const adj: { edgeIdx: number; nodeIdx: number }[][] = nodes.map(() => []);
  edges.forEach((e, idx) => {
    adj[e.a].push({ edgeIdx: idx, nodeIdx: e.b });
    adj[e.b].push({ edgeIdx: idx, nodeIdx: e.a });
  });
  return adj;
}

// Walk a random path through the graph starting at `originIdx`,
// avoiding already-visited nodes so the trace doesn't double back
// on itself. Returns the chain of hops as an array of TraceStep
// records, ready to be rendered with staggered animation delays.
function buildTrace(
  id: number,
  originIdx: number,
  adj: { edgeIdx: number; nodeIdx: number }[][],
  desiredLength: number,
): Trace {
  const visited = new Set<number>([originIdx]);
  const steps: TraceStep[] = [];
  let current = originIdx;
  for (let i = 0; i < desiredLength; i++) {
    // `adj` carries an entry for every node index, populated by
    // buildAdjacency, so we can index into it without a fallback.
    const neighbours = adj[current];
    const candidates = neighbours.filter((n) => !visited.has(n.nodeIdx));
    if (candidates.length === 0) break;
    const pick = candidates[Math.floor(Math.random() * candidates.length)];
    steps.push({
      edgeIdx: pick.edgeIdx,
      fromNodeIdx: current,
      toNodeIdx: pick.nodeIdx,
      delay: i * STEP_MS,
    });
    visited.add(pick.nodeIdx);
    current = pick.nodeIdx;
  }
  // Total time = last hop's start + the longest individual fire
  // animation (edge fire vs trailing node fire), plus a small
  // safety margin so we never yank the overlay mid-animation.
  const lastDelay = steps.length > 0 ? steps[steps.length - 1].delay : 0;
  const totalMs = lastDelay + Math.max(EDGE_FIRE_MS, NODE_FIRE_MS) + 80;
  return { id, originIdx, steps, totalMs };
}

export function LoginBackdropMesh() {
  const reducedMotion = useMemo(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches,
    [],
  );
  const { nodes, edges } = useMemo(buildGraph, []);
  const adjacency = useMemo(() => buildAdjacency(nodes, edges), [nodes, edges]);
  const [traces, setTraces] = useState<Trace[]>([]);
  const idCounter = useRef(0);

  useEffect(() => {
    if (reducedMotion) return;
    let mounted = true;
    const timeouts = new Set<number>();
    const schedule = (cb: () => void, delay: number) => {
      const t = window.setTimeout(() => {
        timeouts.delete(t);
        if (mounted) cb();
      }, delay);
      timeouts.add(t);
    };

    // Restrict trace origins to nodes that sit inside (or only
    // just past) the canonical viewBox, so the user actually
    // sees the very first hop fire instead of the trace entering
    // mid-flight from off-screen.
    const visibleOriginIdxs: number[] = [];
    nodes.forEach((n, i) => {
      if (n.x >= 80 && n.x <= VB_W - 80 && n.y >= 80 && n.y <= VB_H - 80) {
        visibleOriginIdxs.push(i);
      }
    });
    const originPool =
      visibleOriginIdxs.length > 0
        ? visibleOriginIdxs
        : nodes.map((_, i) => i);

    const spawn = () => {
      if (!mounted) return;
      // 1–2 traces fired together so the mesh almost always has at
      // least one independent flow visible, and occasionally two
      // crossing each other — but never enough to flood the page.
      const burst = 1 + Math.floor(Math.random() * 2);
      for (let k = 0; k < burst; k++) {
        const originIdx =
          originPool[Math.floor(Math.random() * originPool.length)];
        // Randomise hop count per trace so flows feel different
        // each time (some short, some longer).
        const length = 5 + Math.floor(Math.random() * 5);
        const id = ++idCounter.current;
        const trace = buildTrace(id, originIdx, adjacency, length);
        if (trace.steps.length === 0) continue;
        setTraces((prev) => [...prev, trace]);
        schedule(
          () => setTraces((prev) => prev.filter((t) => t.id !== trace.id)),
          trace.totalMs + 60,
        );
      }
      // Stagger the next burst so the mesh has quiet beats between
      // bursts rather than one continuous drumbeat.
      schedule(spawn, 1500 + Math.random() * 1200);
    };
    schedule(spawn, 400);

    return () => {
      mounted = false;
      timeouts.forEach((t) => window.clearTimeout(t));
      timeouts.clear();
    };
  }, [reducedMotion, nodes, adjacency]);

  return (
    <Box
      aria-hidden
      sx={{
        position: "absolute",
        inset: 0,
        pointerEvents: "none",
        overflow: "hidden",
      }}
    >
      {/* Keyframes live in a plain <style> tag so Emotion does not
          rename them — the elements below reference these names from
          raw `style={{ animation: ... }}` props rather than through
          Emotion's `sx`/`styled`, so the names must stay verbatim. */}
      <style>{`
        @keyframes lb-mesh-edge {
          0%   { opacity: 0; }
          18%  { opacity: 0.95; }
          70%  { opacity: 0.55; }
          100% { opacity: 0; }
        }
        @keyframes lb-mesh-node {
          0%   { opacity: 0;   r: 2; }
          25%  { opacity: 1;   r: 5; }
          100% { opacity: 0;   r: 7; }
        }
      `}</style>
      <svg
        viewBox={`0 0 ${VB_W} ${VB_H}`}
        preserveAspectRatio="xMidYMid slice"
        style={{ width: "100%", height: "100%", display: "block" }}
      >
        {/* Persistent base mesh — visible regardless of any
            trace, so the topology is always readable as the
            background of the page. */}
        <g>
          {edges.map((e, i) => {
            const a = nodes[e.a];
            const b = nodes[e.b];
            return (
              <line
                key={`e-${i}`}
                x1={a.x}
                y1={a.y}
                x2={b.x}
                y2={b.y}
                stroke="var(--sb-accent)"
                strokeWidth={0.7}
                opacity={0.14}
              />
            );
          })}
          {nodes.map((n, i) => (
            <circle
              key={`n-${i}`}
              cx={n.x}
              cy={n.y}
              r={n.r}
              fill="var(--sb-accent)"
              opacity={0.45}
            />
          ))}
        </g>

        {/* Active traces — one overlay group per live trace, with
            per-element animation delays staggered hop-by-hop. */}
        {traces.map((t) => {
          const origin = nodes[t.originIdx];
          return (
            <g key={t.id}>
              {/* Initial ping at the trace's origin node, so the
                  user can see where each flow starts. */}
              <circle
                cx={origin.x}
                cy={origin.y}
                r={2}
                fill="var(--sb-accent)"
                opacity={0}
                style={{
                  filter: "drop-shadow(0 0 6px var(--sb-accent))",
                  animation: `lb-mesh-node ${NODE_FIRE_MS}ms ease-out 0ms forwards`,
                }}
              />
              {t.steps.map((s, i) => {
                const a = nodes[s.fromNodeIdx];
                const b = nodes[s.toNodeIdx];
                return (
                  <g key={`step-${i}`}>
                    <line
                      x1={a.x}
                      y1={a.y}
                      x2={b.x}
                      y2={b.y}
                      stroke="var(--sb-accent)"
                      strokeWidth={1.7}
                      strokeLinecap="round"
                      opacity={0}
                      style={{
                        animation: `lb-mesh-edge ${EDGE_FIRE_MS}ms ease-out ${s.delay}ms forwards`,
                      }}
                    />
                    {/* Trace "arrives" at the destination node a
                        touch before the edge fully fades, which
                        reads as the packet landing one hop down
                        the line. */}
                    <circle
                      cx={b.x}
                      cy={b.y}
                      r={2}
                      fill="var(--sb-accent)"
                      opacity={0}
                      style={{
                        filter: "drop-shadow(0 0 6px var(--sb-accent))",
                        animation: `lb-mesh-node ${NODE_FIRE_MS}ms ease-out ${
                          s.delay + Math.round(EDGE_FIRE_MS * 0.55)
                        }ms forwards`,
                      }}
                    />
                  </g>
                );
              })}
            </g>
          );
        })}
      </svg>
    </Box>
  );
}
