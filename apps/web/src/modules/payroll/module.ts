import { createElement } from "react";
import type { DepotModule } from "../registry";
import { PayrollHome } from "./pages";

const payroll: DepotModule = {
  id: "payroll",
  title: "Payroll",
  description: "Pay cycles, pay statements and gross-to-net previews.",
  routes: [{ path: "/payroll", element: createElement(PayrollHome) }],
  nav: [{ label: "Pay statement preview", to: "/payroll", duty: "payroll.run.maintain" }],
};

export default payroll;
