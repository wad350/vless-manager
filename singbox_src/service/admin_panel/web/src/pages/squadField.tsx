// Helpers shared by every page that exposes a `squad_ids` field. Keeps the
// FieldSpec and the `string[] → number[]` conversion in one place so the
// pages don't repeat themselves.

import { Box, Chip } from "@mui/material";
import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import type { Api } from "../api/client";
import type { FieldSpec } from "../components/CrudPage";

export interface SquadCatalog {
  // Map keyed by squad id → squad name. Populated on demand via
  // `observeRows`: each refresh of a CRUD table declares which squad
  // ids appear in the visible page and we fetch names only for the
  // subset that isn't already cached.
  names: Map<number, string>;
  // observeRows takes the freshly loaded page of rows, extracts their
  // squad ids via `pick`, and fires a GET /squads?id_in=… for the ids
  // whose names we haven't seen yet. Wired to `CrudConfig.onRowsChange`,
  // which awaits the returned promise before publishing the rows — that
  // way the table only paints once both the row data and the squad
  // names referenced by those rows are in hand, instead of flashing
  // raw ids before the chip labels resolve. Resolves immediately when
  // every visible squad id is already cached (or in flight from a
  // previous refresh).
  observeRows: <TRow>(rows: TRow[], pick: (row: TRow) => number[] | undefined) => Promise<void>;
  // loadOptions fetches every squad and returns the multi-select
  // option list. Intended for CrudDialog's `optionsLoader`, which
  // fires the first time a create dialog is opened — that way the
  // full catalog is only downloaded when the user actually needs to
  // pick squads, not on page mount.
  loadOptions: () => Promise<{ value: string; label: string }[]>;
}

// useSquadCatalog returns the per-page squad-name cache + option loader.
// Unlike the previous `useSquads` the cache starts empty — names are
// populated on demand as rows arrive through `observeRows`, and the
// create-form options are fetched lazily via `loadOptions`. Result:
// visiting e.g. the Users page no longer triggers an unconditional
// GET /squads for the entire squad table.
export function useSquadCatalog(api: Api): SquadCatalog {
  const [names, setNames] = useState<Map<number, string>>(() => new Map());
  // resolvedRef mirrors `names` for synchronous reads inside observeRows
  // (state reads from the hook closure are stale across renders, and we
  // need an up-to-date "is this id known?" check on every call). Kept
  // as a ref because it must never trigger a re-render on its own —
  // visual updates are driven by `names`.
  const resolvedRef = useRef<Set<number>>(new Set());
  // inFlightRef tracks pending GET /squads requests by id so a second
  // observeRows that arrives while the first is still loading shares
  // the same promise instead of either (a) re-firing the request or
  // (b) resolving immediately with names that aren't loaded yet — the
  // latter would defeat the "wait before painting" contract CrudPage
  // relies on.
  const inFlightRef = useRef<Map<number, Promise<void>>>(new Map());

  const observeRows = useCallback(
    async <TRow,>(rows: TRow[], pick: (row: TRow) => number[] | undefined) => {
      const need = new Set<number>();
      for (const row of rows) {
        const ids = pick(row);
        if (!Array.isArray(ids)) continue;
        for (const id of ids) {
          if (!Number.isFinite(id)) continue;
          need.add(id);
        }
      }
      if (need.size === 0) return;

      const waiting: Promise<void>[] = [];
      const fresh: number[] = [];
      for (const id of need) {
        if (resolvedRef.current.has(id)) continue;
        const pending = inFlightRef.current.get(id);
        if (pending) {
          waiting.push(pending);
          continue;
        }
        fresh.push(id);
      }

      if (fresh.length > 0) {
        const request = (async () => {
          try {
            const squads = await api.squads.list({ id_in: fresh });
            if (squads.length > 0) {
              for (const s of squads) resolvedRef.current.add(s.id);
              setNames((prev) => {
                const next = new Map(prev);
                for (const s of squads) next.set(s.id, s.name);
                return next;
              });
            }
            // Ids whose name is missing from the response are still
            // marked resolved — re-asking the server would just
            // produce the same empty answer, and leaving them in
            // limbo would make every subsequent refresh re-fetch
            // them. The chip falls back to the raw id label.
            for (const id of fresh) resolvedRef.current.add(id);
          } catch {
            // Failed fetches stay unresolved so a future refresh can
            // retry — otherwise the chips would remain "1", "2"
            // forever.
          } finally {
            for (const id of fresh) inFlightRef.current.delete(id);
          }
        })();
        for (const id of fresh) inFlightRef.current.set(id, request);
        waiting.push(request);
      }

      if (waiting.length > 0) await Promise.all(waiting);
    },
    [api],
  );

  const loadOptions = useCallback(async () => {
    const squads = await api.squads.list();
    setNames((prev) => {
      const next = new Map(prev);
      for (const s of squads) {
        next.set(s.id, s.name);
        resolvedRef.current.add(s.id);
      }
      return next;
    });
    return squads.map((s) => ({ value: String(s.id), label: s.name }));
  }, [api]);

  return useMemo<SquadCatalog>(
    () => ({ names, observeRows, loadOptions }),
    [names, observeRows, loadOptions],
  );
}

// squadIdsField produces the `FieldSpec` for the squad_ids multi-select
// shown in every create form. The option list is supplied via an
// `optionsLoader` so the catalog is fetched the first time a create
// dialog is opened — not on page mount.
export function squadIdsField(
  loadOptions: () => Promise<{ value: string; label?: string }[]>,
  overrides?: Partial<FieldSpec>,
): FieldSpec {
  return {
    name: "squad_ids",
    label: "Squads",
    type: "multiselect",
    required: true,
    only: "create",
    optionsLoader: loadOptions,
    ...overrides,
  };
}

// parseSquadIds converts whatever the multi-select gave us back into a
// number[] suitable for the API.
export function parseSquadIds(raw: unknown): number[] {
  if (!Array.isArray(raw)) return [];
  return (raw as unknown[])
    .map((v) => Number(v))
    .filter((n) => Number.isFinite(n));
}

// renderSquadIds returns a CrudPage column `render` function that turns
// a `squad_ids: number[]` field into a wrapping row of name chips,
// keyed off the id→name map produced by useSquadCatalog.
export function renderSquadIds<TRow extends { squad_ids?: number[] | null }>(
  names: Map<number, string>,
): (row: TRow) => ReactNode {
  return (row: TRow) => {
    const ids = row.squad_ids;
    if (!Array.isArray(ids) || ids.length === 0) return "";
    return (
      <Box
        sx={{
          display: "flex",
          flexWrap: "wrap",
          rowGap: 0.5,
          columnGap: 0.5,
          maxWidth: "100%",
        }}
      >
        {ids.map((id) => (
          <Chip key={id} label={names.get(id) ?? String(id)} size="small" />
        ))}
      </Box>
    );
  };
}

// pickSquadIds is the default extractor passed to `observeRows` from
// rows whose squad membership sits in a canonical `squad_ids` field.
// Exposed so pages don't have to inline the same `(row) => row.squad_ids`.
export function pickSquadIds<TRow extends { squad_ids?: number[] | null }>(
  row: TRow,
): number[] | undefined {
  return row.squad_ids ?? undefined;
}
