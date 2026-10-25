import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LineChart } from "./LineChart";

const ticks = (c: HTMLElement) =>
  [...c.querySelectorAll(".az-chart__scale span")].map((s) => s.textContent);

describe("LineChart integer axis", () => {
  it("labels a maximum of 1 with whole numbers and no repeats", () => {
    const { container } = render(
      <LineChart integer series={[{ data: [0, 0, 1] }]} />,
    );
    expect(ticks(container)).toEqual(["1", "0"]);
  });

  it("steps by whole numbers for larger counts", () => {
    const { container } = render(
      <LineChart integer series={[{ data: [0, 9] }]} />,
    );
    const t = ticks(container);
    expect(t).toEqual(["9", "6", "3", "0"]);
    expect(new Set(t).size).toBe(t.length);
  });

  it("keeps the proportional axis by default", () => {
    const { container } = render(<LineChart series={[{ data: [0, 1] }]} />);
    expect(ticks(container)).toHaveLength(5);
  });
});
