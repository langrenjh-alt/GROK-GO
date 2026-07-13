import { render, screen } from "@testing-library/react";
import axe from "axe-core";
import { Input, Select, Switch } from "./form";

describe("form controls", () => {
  it("associates labels, descriptions, and validation errors", () => {
    render(<Input id="project" label="Project Name" error="Project name is required." />);
    const input = screen.getByLabelText("Project Name");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription("Project name is required.");
  });

  it("has no detectable accessibility violations", async () => {
    const { container } = render(
      <form>
        <Input id="name" label="Name" description="A display name." />
        <Select id="region" label="Region" options={[{ value: "hnd", label: "Tokyo" }]} />
        <Switch label="Enabled" checked onCheckedChange={() => undefined} />
      </form>,
    );
    const result = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
    expect(result.violations).toEqual([]);
  });
});
