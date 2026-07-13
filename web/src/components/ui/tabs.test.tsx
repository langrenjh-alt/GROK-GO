import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./tabs";

describe("Tabs", () => {
  it("supports keyboard navigation and exposes the active panel", async () => {
    const user = userEvent.setup();
    render(
      <Tabs defaultValue="json">
        <TabsList aria-label="Body format">
          <TabsTrigger value="json">JSON</TabsTrigger>
          <TabsTrigger value="multipart">Multipart</TabsTrigger>
        </TabsList>
        <TabsContent value="json">JSON editor</TabsContent>
        <TabsContent value="multipart">Multipart editor</TabsContent>
      </Tabs>,
    );

    const json = screen.getByRole("tab", { name: "JSON" });
    const multipart = screen.getByRole("tab", { name: "Multipart" });
    expect(json).toHaveAttribute("data-state", "active");
    expect(screen.getByText("JSON editor")).toBeVisible();

    await user.click(json);
    await user.keyboard("{ArrowRight}");
    expect(multipart).toHaveAttribute("data-state", "active");
    expect(screen.getByText("Multipart editor")).toBeVisible();
  });
});
