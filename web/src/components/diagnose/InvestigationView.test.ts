import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { InvestigationStartErrorAlert } from "./InvestigationView";

it("renders new-run failures as a pane-independent workspace alert", () => {
  const html = renderToStaticMarkup(
    createElement(InvestigationStartErrorAlert, {
      error: "The run could not be created.",
      onDismiss: () => {},
    }),
  );
  expect(html).toContain('role="alert"');
  expect(html).toContain("Couldn&#x27;t start a new investigation");
  expect(html).toContain("The run could not be created.");
  expect(html).not.toContain('class="hidden');
});
