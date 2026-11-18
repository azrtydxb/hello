import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Conference } from "../api";
import { RoomDrawer } from "./ConferenceForm";

// The form alone, signed in as an operator.
vi.mock("../role", () => ({
  Can: ({ children }: { children: ReactNode }) => children,
  useCan: () => true,
  useRole: () => "operator",
}));

const room = {
  id: 4,
  name: "weekly",
  number: "800",
  maxParticipants: 10,
  waitForLeader: false,
  muteOnEntry: false,
  entryExit: "tone",
  entryAnnouncement: "",
  exitAnnouncement: "",
  record: false,
  enabled: true,
  hasUserPin: true,
  hasLeaderPin: false,
} as unknown as Conference;

describe("the conference form on the canvas", () => {
  it("leaves PINs to the Conferences page, so a draft never holds one", async () => {
    const commit = vi.fn(() => Promise.resolve());
    render(
      <RoomDrawer
        room={room}
        commit={commit}
        onClose={() => undefined}
        onSaved={() => undefined}
      />,
    );
    expect(screen.queryByLabelText("User PIN")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Leader PIN")).not.toBeInTheDocument();
    expect(
      screen.getByText(/PINs are set on the Conferences page/),
    ).toBeInTheDocument();
    fireEvent.submit(screen.getByRole("form"));
    await waitFor(() => expect(commit).toHaveBeenCalledTimes(1));
    const body = JSON.stringify(commit.mock.calls[0]);
    expect(body).not.toMatch(/pin/i);
  });

  it("still sets PINs on its own page", () => {
    render(
      <RoomDrawer
        room={room}
        onClose={() => undefined}
        onSaved={() => undefined}
      />,
    );
    expect(screen.getByLabelText("User PIN")).toBeInTheDocument();
  });
});
