# Workflow module

My work (`/workflow/inbox`) reads `/api/v1/inbox` and decides items through the shell inbox client. The job monitor, designer and templates routes are in `src/shell/plannedRoutes.json` and render an honest "isn't available yet" page until the batch service can list runs, definitions and templates; the demo rows for them wait in `src/demo/handlers/workflow.ts`.

Needs: none beyond the shell lockfile.
