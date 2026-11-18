import { useCallback } from "react";
import { Link } from "react-router-dom";
import { z } from "zod";
import { defaultFetch, requestUrl, type FetchLike } from "../../shell/api/http";
import { EntityName } from "../../shell/lookup/EntityName";
import { buttonClassName, Card, EmptyState, ListPage, PageHeader, entityText, usePagedRows, type ListColumn, type ListData, type RowPage } from "../../shell/ui";

/** GET /v1/numbering/sequences (services/numbering/api/openapi.yaml, listSequences). */
export const SEQUENCES_PATH = "/api/v1/numbering/sequences";
export const PAGE_SIZE = 100;

const sequenceSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  format: z.string(),
  legalEntityId: z.string(),
  mode: z.string(),
  startFrom: z.number(),
  nextValue: z.number(),
  nextFormatted: z.string().optional(),
});

export type Sequence = z.infer<typeof sequenceSchema>;

const pageSchema = z.object({ items: z.array(sequenceSchema), nextCursor: z.string().optional() });

export async function listSequences(cursor: string | undefined, fetchImpl: FetchLike = defaultFetch, signal?: AbortSignal): Promise<RowPage<Sequence>> {
  const query = new URLSearchParams({ limit: String(PAGE_SIZE) });
  if (cursor) query.set("cursor", cursor);
  return requestUrl("GET", `${SEQUENCES_PATH}?${query.toString()}`, pageSchema, fetchImpl, {}, undefined, signal);
}

const MODE_LABEL: Record<string, string> = {
  gap_free: "Gap-free",
  continuous: "Continuous",
  non_continuous: "Non-continuous",
};

export function modeLabel(mode: string): string {
  return MODE_LABEL[mode] ?? mode.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
}

/** The number the next document gets, in the sequence's format when it still fits. */
export function nextNumber(row: Sequence): string {
  return row.nextFormatted ?? String(row.nextValue);
}

/**
 * True when the list route itself is missing: a 404 or 405 with no problem body, which readProblem turns into
 * COM-0500. That happens only against a numbering service older than its list route; trying again cannot help.
 */
export function listRouteMissing(error: unknown): boolean {
  const { status, code } = (error ?? {}) as { status?: unknown; code?: unknown };
  return (status === 404 || status === 405) && (code === undefined || code === "COM-0500");
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
  { key: "nextValue", header: "Next number", mono: true, render: nextNumber, sortValue: (row) => row.nextValue, csv: nextNumber },
  { key: "startFrom", header: "Starts at", align: "end", defaultHidden: true },
];

const TITLE = "Number sequences";
const DESCRIPTION = "The sequences that number your documents, for each legal entity, and the next number each will give. You can't create or change sequences on this page yet.";

/**
 * The number sequences of every legal entity the person can see, read-only, with the next number each will give. The
 * setup checklist's sequences step links here (modules/setup/checks.ts). Creating and changing sequences is B-35.
 */
export function SequencesPage() {
  const fetchPage = useCallback((cursor: string | undefined, signal: AbortSignal) => listSequences(cursor, defaultFetch, signal), []);
  const data: ListData<Sequence> = usePagedRows("admin-sequences", fetchPage);
  if (listRouteMissing(data.error)) {
    // A permanent state, not a failure: no Try again, and no search, views or export over a list that cannot load.
    return (
      <>
        <PageHeader title={TITLE} description={DESCRIPTION} />
        <Card>
          <EmptyState
            icon="info"
            headingLevel={2}
            title="Number sequences can't be listed yet"
            description="This installation of Depot can't list number sequences yet, so there is nothing to show here. Documents are still numbered as before. Ask your administrator about updating Depot."
          />
        </Card>
      </>
    );
  }
  return (
    <ListPage
      id="admin-sequences"
      title={TITLE}
      description={DESCRIPTION}
      noun="number sequences"
      singular="number sequence"
      columns={columns}
      useRows={() => data}
      getRowKey={(row) => row.id}
      search={(row) => ({ number: row.code, name: row.name })}
      empty={{
        title: "No number sequences yet",
        description: "Number sequences give your documents their numbers, for each legal entity. Creating them here isn't available yet, so ask support to set them up. The setup checklist shows what else is missing.",
        action: (
          <Link to="/setup/checklist" className={buttonClassName("secondary")}>
            Open the setup checklist
          </Link>
        ),
      }}
    />
  );
}
