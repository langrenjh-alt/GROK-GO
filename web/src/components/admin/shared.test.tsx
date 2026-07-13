import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PageFooter } from "./shared";

describe("PageFooter", () => {
  it("shows the server range and emits paging changes", async () => {
    const user = userEvent.setup();
    const onPageChange = vi.fn();
    const onPageSizeChange = vi.fn();
    render(<PageFooter count={25} noun="accounts" total={61} page={1} pageSize={25} onPageChange={onPageChange} onPageSizeChange={onPageSizeChange} />);

    expect(screen.getByText("1-25 / 61 accounts")).toBeVisible();
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Next page" }));
    expect(onPageChange).toHaveBeenCalledWith(2);
    await user.selectOptions(screen.getByLabelText("Rows per page"), "50");
    expect(onPageSizeChange).toHaveBeenCalledWith(50);
  });
});
