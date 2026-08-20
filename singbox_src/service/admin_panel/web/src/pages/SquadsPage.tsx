import GroupsIcon from "@mui/icons-material/Groups";
import { useMemo } from "react";
import { CrudPage, type CrudConfig } from "../components/CrudPage";
import { useApi } from "../auth/AuthContext";
import type { Squad, SquadCreate, SquadUpdate } from "../api/types";

export function SquadsPage() {
  const api = useApi();
  // Memoise so CrudPage's `reload` callback (which depends on `config`)
  // keeps a stable identity across re-renders. Without this every parent
  // render would invalidate `reload` and refire the list+count requests.
  const config = useMemo<CrudConfig<Squad, SquadCreate, SquadUpdate>>(
    () => ({
      title: "Squads",
      icon: <GroupsIcon />,
      idKey: "id",
      columns: [
        { key: "id", label: "ID" },
        { key: "name", label: "Name" },
        { key: "created_at", label: "Created At" },
        { key: "updated_at", label: "Updated At" },
      ],
      filters: [
        { name: "name", label: "Name", type: "text" },
        { name: "created_at", label: "Created at", type: "datetime-range" },
        { name: "updated_at", label: "Updated at", type: "datetime-range" },
      ],
      fields: [{ name: "name", label: "Name", type: "text", required: true }],
      list: (q) => api.squads.list(q),
      count: (q) => api.squads.count(q),
      create: (b) => api.squads.create(b),
      update: (id, b) => api.squads.update(Number(id), b),
      remove: (id) => api.squads.remove(Number(id)),
      toCreate: (f) => ({ name: String(f.name ?? "") }),
      toUpdate: (f) => ({ name: String(f.name ?? "") }),
    }),
    [api],
  );
  return <CrudPage<Squad, SquadCreate, SquadUpdate> config={config} />;
}
