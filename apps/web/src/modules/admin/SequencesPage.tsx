import { useQuery } from "@tanstack/react-query";
import { z } from "zod";
import { defaultFetch, requestUrl, type FetchLike } from "../../shell/api/http";
import { EntityName } from "../../shell/lookup/EntityName";
import { ListPage, entityText, isForbidden, loadedRows, type ListColumn } from "../../shell/ui";

/** The read path of the numbering service (services/numbering/api/openapi.yaml, schema Sequence). */
export const SEQUENCES_PATH = "/api/v1/numbering/sequences";

const MODES = ["gap_free", "continuous", "non_continuous"] as const;

const sequenceSchema = z.object({
  id: z.string().optional(),
  code: z.string(),
  name: z.string(),
  format: z.string(),
  legalEntityId: z.string(),
  mode: z.string(),
  startFrom: z.number().optional(),
});

export type Sequence = z.infer<typeof sequenceSchema>;

// The list arrives either as a bare array or as `{ items }`, the way the setup checklist reads it (modules/setup/checks.ts).
const listSchema = z.union([z.array(sequenceSchema), z.object({ items: z.array(sequenceSchema) })]);

export async function listSequences(fetchImpl: FetchLike = defaultFetch, signal?: AbortSignal): Promise<Sequence[]> {
  const body = await requestUrl("GET", SEQUENCES_PATH, listSchema, fetchImpl, {}, undefined, signal);
  return Array.isArray(body) ? body : body.items;
}

const MODE_LABEL: Record<(typeof MODES)[number], string> = {
  gap_free: "Gap-free",
  continuous: "Continuous",
  non_continuous: "Non-continuous",
};

export function modeLabel(mode: string): string {
  return (MODE_LABEL as Record<string, string>)[mode] ?? mode.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
}

/**
 * A missing list route answers 404 or 405 with no problem body, which readProblem turns into COM-0500 and would read
 * as "something went wrong on our side, try again". Trying again cannot help, so say what is true instead. A 404 or
 * 405 that carries its own problem code keeps the catalog message.
 */
export function explainFailure(error: unknown): unknown {
  const { status, code, requestId } = (error ?? {}) as { status?: unknown; code?: unknown; requestId?: unknown };
  if ((status !== 404 && status !== 405) || (code !== undefined && code !== "COM-0500")) return error;
  return {
    status,
    requestId,
    detail: "Depot can't list number sequences yet, so there is nothing to show here. Documents are still numbered as before.",
  };
}

const columns: ListColumn<Sequence>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  {
    key: "legalEntityId",
    header: "Legal entity",
    render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} />,
    sortValue: (row) => row.legalEntityId,
    csv: (row) => entityText("legalEntity", row.legalEntityId),
  },
  { key: "format", header: "Format", mono: true },
  { key: "mode", header: "Numbering", render: (row) => modeLabel(row.mode), csv: (row) => modeLabel(row.mode) },
  { key: "startFrom", header: "Starts at", align: "end", render: (row) => (row.startFrom === undefined ? "" : String(row.startFrom)) },
];

function useRows() {
  const query = useQuery({ queryKey: ["admin", "numbering", "sequences"], queryFn: ({ signal }) => listSequences(defaultFetch, signal), retry: false });
  const error = query.isError ? query.error : undefined;
  return loadedRows(
    { data: query.data, loading: query.isPending, error: error === undefined ? undefined : explainFailure(error), reload: () => void query.refetch() },
    isForbidden(error),
  );
}

/**
 * The number sequences of every legal entity the person can see, read-only. The setup checklist's sequences step
 * links here (modules/setup/checks.ts). Creating and changing sequences is a later page.
 */
export function SequencesPage() {
  return (
    <ListPage
      id="admin-sequences"
      title="Number sequences"
      description="The sequences that number your documents, for each legal entity. You can't create or change sequences on this page yet."
      noun="number sequences"
      singular="number sequence"
      columns={columns}
      useRows={useRows}
      getRowKey={(row) => `${row.legalEntityId}/${row.code}`}
      search={(row) => ({ number: row.code, name: row.name })}
      empty={{
        title: "No number sequences yet",
        description: "Number sequences give your documents their numbers, for each legal entity. Creating them on this page isn't available yet.",
      }}
    />
  );
}
