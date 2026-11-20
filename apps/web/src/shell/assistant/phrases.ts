import type { PromptKind } from "./router";

export const CANONICAL_PHRASES: Readonly<Record<PromptKind, readonly string[]>> = {
  ask: ["what is the total of our overdue invoices over $10k?"],
  navigate: ["show me overdue invoices over $10k", "create a PO for 50 widgets from Acme"],
  act: ["apply this payment to invoice INV-1042", "cancel this planned order"],
};
