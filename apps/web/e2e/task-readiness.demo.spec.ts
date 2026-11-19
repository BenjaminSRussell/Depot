import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";
import { loadTasks, OTHER_APPS, plannedIssue } from "./usabilityTasks";

const RECORD_FEEDS: Record<string, string> = {
  "/sales/orders/:id": "/api/v1/sales/orders",
  "/party/:id": "/api/v1/party/parties:search?q=Ironbridge",
  "/engineering/boms/:id": "/api/v1/engineering/bom-versions",
  "/shopfloor/kiosk/pi/:id": "/api/v1/shopfloor/control-recipes",
};

test.describe("task readiness: every task can start in the demo build", () => {
  for (const task of loadTasks()) {
    test(`${task.id} (${task.persona}) starts at ${task.route}`, async ({ page }) => {
      const issue = plannedIssue(task.route);
      test.skip(issue !== undefined, `not measurable: ${task.route} is planned (issue #${issue})`);
      test.skip(task.module in OTHER_APPS, `not measurable here: ${OTHER_APPS[task.module]}`);

      const shell = new ShellPage(page);
      await shell.goto("./");
      let route = task.route;
      const feed = RECORD_FEEDS[task.route];
      if (feed) {
        const firstId = await page.evaluate(async (url) => {
          const body = (await (await fetch(url)).json()) as { items?: Array<{ id?: string }> };
          return body.items?.[0]?.id;
        }, feed);
        expect(firstId, `${task.id}: the demo company has no record behind ${feed}`).toBeTruthy();
        route = task.route.replace(":id", firstId as string);
      }
      await shell.goto(`.${route}`);

      await expect(shell.title.first()).toBeVisible();
      // The heading renders with the loading skeleton, so wait for the page to finish loading before
      // checking for the error: a query that fails after first paint must fail this task. No catch: a page
      // that never finishes loading is a failure too.
      await page.getByText("Loading…").first().waitFor({ state: "detached", timeout: 10_000 });
      await expect(page.getByText("This page couldn't load")).toHaveCount(0);
    });
  }
});
