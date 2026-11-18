import { createElement } from "react";
import type { DepotModule } from "../registry";
import { CustomerContactsPage } from "./security/CustomerContactsPage";
import { EffectivePermissionsPage } from "./security/EffectivePermissionsPage";
import { LegalEntitiesPage } from "./security/LegalEntitiesPage";
import { RoleAssignmentsPage } from "./security/RoleAssignmentsPage";
import { RoleDetailPage, RolesPage } from "./security/RolesPage";
import { UserDetailPage, UsersPage } from "./security/UsersPage";
import { ImportPage } from "./ImportPage";
import { AdminHome } from "./pages";
import { plannedRoute } from "../../shell/pages/PlannedPage";
import { SequencesPage } from "./SequencesPage";

const admin: DepotModule = {
  id: "admin",
  title: "Admin",
  description: "Number sequences, imports, users and roles.",
  routes: [
    { path: "/admin", element: createElement(AdminHome) },
    plannedRoute("/admin/audit"),
    { path: "/admin/sequences", element: createElement(SequencesPage) },
    plannedRoute("/admin/alerts"),
    { path: "/admin/import", element: createElement(ImportPage) },
    plannedRoute("/admin/documents"),
    plannedRoute("/admin/texts"),
    { path: "/admin/security/permissions", element: createElement(EffectivePermissionsPage), handle: { title: "Effective permissions" } },
    { path: "/admin/security/assignments", element: createElement(RoleAssignmentsPage), handle: { title: "Role assignments" } },
    { path: "/admin/security/legal-entities", element: createElement(LegalEntitiesPage), handle: { title: "Legal entities" } },
    { path: "/admin/security/users", element: createElement(UsersPage), handle: { title: "Users" } },
    { path: "/admin/security/users/:userId", element: createElement(UserDetailPage), handle: { title: "User" } },
    { path: "/admin/security/customer-contacts", element: createElement(CustomerContactsPage), handle: { title: "Customer portal contacts" } },
    { path: "/admin/security/roles", element: createElement(RolesPage), handle: { title: "Role catalogue" } },
    { path: "/admin/security/roles/:name", element: createElement(RoleDetailPage), handle: { title: "Role" } },
  ],
  // Entries for routes in shell/plannedRoutes.json stay here for the nav audit and the page title, but the shell drops
  // them from the sidebar, workspaces, All features and the palette (shell/pages/PlannedPage.tsx, withoutPlannedNav).
  nav: [
    { label: "Overview", to: "/admin", duty: "audit.read" },
    { label: "Audit", to: "/admin/audit", duty: "audit.read" },
    { label: "Number sequences", to: "/admin/sequences", duty: "numbering.read" },
    { label: "Alerts", to: "/admin/alerts", duty: "audit.read" },
    { label: "Import records", to: "/admin/import" },
    { label: "Documents", to: "/admin/documents" },
    { label: "Standard texts", to: "/admin/texts" },
    { label: "Effective permissions", to: "/admin/security/permissions", duty: "identity.effective-permission.view" },
    { label: "Role assignments", to: "/admin/security/assignments", duty: "identity.security-role.maintain" },
    { label: "Legal entities", to: "/admin/security/legal-entities", duty: "identity.legal-entity.maintain" },
    { label: "Users", to: "/admin/security/users", duty: "identity.user.maintain" },
    { label: "Portal contacts", to: "/admin/security/customer-contacts", duty: "identity.customer-contact.maintain" },
    { label: "Role catalogue", to: "/admin/security/roles", duty: "identity.security-role.maintain" },
  ],
};

export default admin;
