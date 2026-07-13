import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button, IconButton } from "./button";
import { Copy } from "lucide-react";

describe("Button", () => {
  it("announces and blocks the loading state", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(<Button loading onClick={onClick}>Save Changes</Button>);
    const button = screen.getByRole("button", { name: "Save Changes" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    await user.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("requires an explicit accessible label for icon controls", () => {
    render(<IconButton label="Copy API key"><Copy /></IconButton>);
    expect(screen.getByRole("button", { name: "Copy API key" })).toBeVisible();
  });
});
