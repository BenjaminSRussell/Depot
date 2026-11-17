import { createElement } from "react";
import type { DepotModule } from "../registry";
import { DocumentDesignPage } from "./DocumentDesignPage";
import { NewOutputRulePage } from "./OutputRuleFormPage";
import { NewStatementSchedulePage } from "./ScheduleFormPage";
import { NewTemplatePage, TemplatePage } from "./TemplateFormPage";
import { TemplateHistoryPage } from "./TemplateHistoryPage";
import { NewTextBlockPage, TextBlockPage } from "./TextBlockFormPage";

const documentDesign: DepotModule = {
  id: "document-design",
  title: "Document design",
  description: "Document templates, standard text blocks with language versions, and the output rules that route the original and the copies of each business document.",
  routes: [
    { path: "/document-design", element: createElement(DocumentDesignPage), handle: { title: "Document design", duty: "print.job.submit" } },
    { path: "/document-design/templates/new", element: createElement(NewTemplatePage), handle: { title: "New document template", duty: "print.admin" } },
    { path: "/document-design/templates/:code", element: createElement(TemplatePage), handle: { title: "Document template", duty: "print.job.submit" } },
    { path: "/document-design/templates/:code/versions", element: createElement(TemplateHistoryPage), handle: { title: "Template version history", duty: "print.job.submit" } },
    { path: "/document-design/text-blocks/new", element: createElement(NewTextBlockPage), handle: { title: "New text block", duty: "print.admin" } },
    { path: "/document-design/text-blocks/:code/:language", element: createElement(TextBlockPage), handle: { title: "Text block", duty: "print.job.submit" } },
    { path: "/document-design/output-rules/new", element: createElement(NewOutputRulePage), handle: { title: "New output rule", duty: "print.admin" } },
    { path: "/document-design/statement-schedules/new", element: createElement(NewStatementSchedulePage), handle: { title: "New statement schedule", duty: "print.admin" } },
  ],
  nav: [{ label: "Document design", to: "/document-design", duty: "print.job.submit" }],
};

export default documentDesign;
