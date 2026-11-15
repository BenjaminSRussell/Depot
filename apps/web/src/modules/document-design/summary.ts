import type { TemplateConfig } from "./api";

/** What a template changes, in words: "1 hidden columns, 1 form notes", or "None". */
export function changesSummary(config: TemplateConfig): string {
  const parts = [
    config.hideColumns?.length ? `${config.hideColumns.length} hidden columns` : "",
    config.renameColumns && Object.keys(config.renameColumns).length ? `${Object.keys(config.renameColumns).length} renamed columns` : "",
    config.noteBlocks?.length ? `${config.noteBlocks.length} form notes` : "",
    config.bodyBlock ? "letter text" : "",
    config.footerText ? "footer line" : "",
  ].filter(Boolean);
  return parts.length ? parts.join(", ") : "None";
}
