import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { HelloLogo } from "./brand";

describe("HelloLogo", () => {
  // Catches the emblem artwork being dropped (ProductLogo would fall back to
  // its dashed placeholder) or the dark and light images being swapped.
  it("shows the dark and light emblems, not the placeholder", () => {
    const { container } = render(<HelloLogo layout="stacked" size={220} />);
    expect(screen.getByRole("img", { name: "Kuvryn Hello" })).toBeVisible();
    expect(container.querySelector(".az-plogo__ph")).toBeNull();
    const dark = container.querySelector(".az-plogo__em--dark");
    const light = container.querySelector(".az-plogo__em--light");
    expect(dark?.getAttribute("src")).toMatch(/kh-emblem-dark\.webp/);
    expect(light?.getAttribute("src")).toMatch(/kh-emblem-light\.webp/);
  });
});
