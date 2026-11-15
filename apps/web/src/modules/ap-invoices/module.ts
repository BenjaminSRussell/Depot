import { createElement } from "react";
import type { PassageModule } from "../registry";
import { GrirPage } from "./GrirPage";
import { IntakeDetailPage } from "./IntakeDetailPage";
import { IntakePage } from "./IntakePage";
import { InvoiceDetailPage } from "./InvoiceDetailPage";
import { InvoicesPage } from "./InvoicesPage";
import { PostInvoicePage } from "./PostInvoicePage";
import { SubmissionDetailPage } from "./SubmissionDetailPage";
import { SubmissionsPage } from "./SubmissionsPage";

const apInvoices: PassageModule = {
  id: "ap-invoices",
  title: "AP invoices",
  description: "See goods received and not yet invoiced, post vendor invoices against them, review invoices vendors submitted in the supplier portal, match the invoices that arrived electronically to their orders and receipts, and look up what was posted.",
  routes: [
    { path: "/ap-invoices", element: createElement(InvoicesPage), handle: { title: "Vendor invoices" } },
    { path: "/ap-invoices/grir", element: createElement(GrirPage), handle: { title: "GR/IR open items" } },
    { path: "/ap-invoices/post", element: createElement(PostInvoicePage), handle: { title: "Post vendor invoice" } },
    { path: "/ap-invoices/submissions", element: createElement(SubmissionsPage), handle: { title: "Invoice submissions", duty: "receiving.ap.inquire" } },
    { path: "/ap-invoices/submissions/:id", element: createElement(SubmissionDetailPage), handle: { title: "Invoice submission", duty: "receiving.ap.inquire" } },
    { path: "/ap-invoices/intake", element: createElement(IntakePage), handle: { title: "Invoices received electronically", duty: "open-item.read" } },
    { path: "/ap-invoices/intake/:id", element: createElement(IntakeDetailPage), handle: { title: "Received invoice", duty: "open-item.read" } },
    { path: "/ap-invoices/:id", element: createElement(InvoiceDetailPage), handle: { title: "Vendor invoice" } },
  ],
  nav: [
    { label: "Vendor invoices", to: "/ap-invoices", duty: "receiving.ap.inquire" },
    { label: "GR/IR open items", to: "/ap-invoices/grir", duty: "receiving.ap.inquire" },
    { label: "Post vendor invoice", to: "/ap-invoices/post", duty: "receiving.vendor-invoice.post" },
    { label: "Invoice submissions", to: "/ap-invoices/submissions", duty: "receiving.ap.inquire" },
    { label: "Invoices received electronically", to: "/ap-invoices/intake", duty: "open-item.read" },
  ],
};

export default apInvoices;
