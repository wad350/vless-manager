import { Chip } from "@mui/material";
import StorageIcon from "@mui/icons-material/Storage";
import { useEffect, useMemo, useState } from "react";
import { CrudPage, type CrudConfig } from "../components/CrudPage";
import { CopyableId } from "../components/CopyableId";
import { useApi } from "../auth/AuthContext";
import type { Node, NodeCreate, NodeStatus, NodeUpdate } from "../api/types";
import {
  parseSquadIds,
  pickSquadIds,
  renderSquadIds,
  squadIdsField,
  useSquadCatalog,
} from "./squadField";

function StatusChip({ uuid }: { uuid: string }) {
  const api = useApi();
  const [status, setStatus] = useState<NodeStatus | "loading" | "error">("loading");
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const s = await api.nodes.status(uuid);
        if (!cancelled) setStatus(s);
      } catch {
        if (!cancelled) setStatus("error");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [api, uuid]);
  const color: "default" | "success" | "warning" | "error" =
    status === "online" ? "success" : status === "offline" ? "warning" : status === "error" ? "error" : "default";
  return <Chip size="small" color={color} label={status === "loading" ? "…" : status} />;
}

export function NodesPage() {
  const api = useApi();
  const squads = useSquadCatalog(api);
  // Memoise the CRUD config so CrudPage's `reload` callback stays stable
  // across re-renders. Recomputed when the API client flips or a new
  // squad name is merged into the catalog through observeRows.
  const config = useMemo<CrudConfig<Node, NodeCreate, NodeUpdate>>(() => ({
    title: "Nodes",
    icon: <StorageIcon />,
    idKey: "uuid",
    rowKey: (r) => r.uuid,
    onRowsChange: (rows) => squads.observeRows(rows, pickSquadIds),
    columns: [
      {
        key: "uuid",
        label: "UUID",
        render: (row) => <CopyableId value={row.uuid} />,
      },
      { key: "name", label: "Name" },
      {
        key: "squad_ids",
        label: "Squads",
        sortable: false,
        render: renderSquadIds<Node>(squads.names),
      },
      {
        // Status is computed live per-row via /nodes/:uuid/status, so the
        // server can't sort by it.
        key: "status",
        label: "Status",
        sortable: false,
        render: (row) => <StatusChip uuid={row.uuid} />,
      },
      { key: "created_at", label: "Created at" },
      { key: "updated_at", label: "Updated at" },
    ],
    filters: [
      { name: "uuid", label: "UUID", type: "text", wide: true },
      { name: "name", label: "Name", type: "text" },
      { name: "created_at", label: "Created at", type: "datetime-range" },
      { name: "updated_at", label: "Updated at", type: "datetime-range" },
    ],
    fields: [
      { name: "uuid", label: "UUID", type: "uuid", required: true, only: "create" },
      { name: "name", label: "Name", type: "text", required: true },
      squadIdsField(squads.loadOptions),
    ],
    list: (q) => api.nodes.list(q),
    count: (q) => api.nodes.count(q),
    create: (b) => api.nodes.create(b),
    update: (id, b) => api.nodes.update(String(id), b),
    remove: (id) => api.nodes.remove(String(id)),
    toCreate: (f) => ({
      uuid: String(f.uuid ?? "").trim(),
      name: String(f.name ?? "").trim(),
      squad_ids: parseSquadIds(f.squad_ids),
    }),
    toUpdate: (f) => ({ name: String(f.name ?? "").trim() }),
  }), [api, squads]);
  return <CrudPage<Node, NodeCreate, NodeUpdate> config={config} />;
}
