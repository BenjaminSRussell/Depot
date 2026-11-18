import { useInfiniteQuery } from "@tanstack/react-query";
import { z } from "zod";
import { ApiError, defaultFetch, requestUrl } from "../../shell/api/http";
import { ProblemMessage } from "../../shell/errors/ProblemMessage";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Button, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";

const sequenceSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  format: z.string(),
  legalEntityId: z.string(),
  mode: z.enum(["gap_free", "continuous", "non_continuous"]),
  startFrom: z.number(),
});

const pageSchema = z.object({
  items: z.array(sequenceSchema),
  next: z.string().optional(),
});

type Sequence = z.infer<typeof sequenceSchema>;

const URL_PATH = "/api/v1/numbering/sequences";

const modeLabel: Record<Sequence["mode"], string> = {
  gap_free: "Gap-free",
  continuous: "Continuous",
  non_continuous: "Non-continuous",
};

const columns: DataTableColumn<Sequence>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
  { key: "format", header: "Format", mono: true },
  { key: "mode", header: "Numbering", render: (row) => modeLabel[row.mode] },
  { key: "startFrom", header: "Starts at", align: "end" },
];

export function SequencesPage() {
  const sequences = useInfiniteQuery({
    queryKey: ["admin", "numbering-sequences"],
    queryFn: ({ pageParam, signal }) =>
      requestUrl("GET", pageParam ? `${URL_PATH}?after=${encodeURIComponent(pageParam)}` : URL_PATH, pageSchema, defaultFetch, {}, undefined, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next || undefined,
    retry: false,
  });
  const forbidden = sequences.error instanceof ApiError && sequences.error.status === 403;
  const rows = sequences.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <>
      <PageHeader title="Sequences" description="How your documents are numbered, for each legal entity. You can see them here; changing them is not available yet." />
      {sequences.isError ? (
        forbidden ? (
          <Alert tone="info" title="You can't see the number sequences">
            Your account doesn't have access to them. Ask an administrator if you should.
          </Alert>
        ) : (
          <Alert
            tone="danger"
            live="alert"
            title="Could not load the number sequences"
            action={
              <Button variant="secondary" loading={sequences.isFetching} onClick={() => void sequences.refetch()}>
                Try again
              </Button>
            }
          >
            <ProblemMessage problem={sequences.error} fallback="The number sequences could not be loaded." />
          </Alert>
        )
      ) : (
        <>
          <DataTable
            label="Number sequences"
            columns={columns}
            rows={rows}
            getRowKey={(row) => row.id}
            loading={sequences.isPending}
            empty={{ title: "No number sequences yet", description: "Sequences number your documents without gaps, per legal entity." }}
          />
          {sequences.hasNextPage ? (
            <Button variant="secondary" loading={sequences.isFetchingNextPage} onClick={() => void sequences.fetchNextPage()}>
              Load more
            </Button>
          ) : null}
        </>
      )}
    </>
  );
}
