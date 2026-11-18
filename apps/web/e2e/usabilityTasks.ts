import plannedRoutes from "../src/shell/plannedRoutes.json" with { type: "json" };
import tasksJson from "../src/shell/usabilityTasks.json" with { type: "json" };

export interface UsabilityTask {
  id: string;
  persona: string;
  route: string;
  module: string;
}

export const OTHER_APPS: Record<string, string> = {
  device: "the handheld device app runs only in the development stub (DEPOT_MOBILE_STUB=1)",
  "customer-portal": "the customer portal is its own app (apps/customer-portal, demo build)",
  "supplier-portal": "the supplier portal is its own app (apps/supplier-portal, demo build)",
};

export function loadTasks(): UsabilityTask[] {
  return Object.entries(tasksJson.personas).flatMap(([persona, { tasks }]) =>
    Object.entries(tasks).map(([id, [route, module]]) => ({ id, persona, route, module })),
  );
}

export function plannedIssue(route: string): number | undefined {
  return plannedRoutes.find((p) => p.route === route)?.issue;
}
