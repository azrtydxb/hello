import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { IVR_DESTINATION_KINDS } from "../api";
import type { VoicemailGroup } from "../api/vmgroups";
import {
  DestinationPicker,
  targetOptions,
  type DestinationChoices,
} from "./IvrDestination";

const group = (number: string, name: string): VoicemailGroup => ({
  id: Number(number),
  number,
  name,
  enabled: true,
  maxSeconds: 180,
  members: [],
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
});

const choices = (groups?: VoicemailGroup[]): DestinationChoices => ({
  extensions: [],
  ringGroups: [],
  announcements: [],
  pagingGroups: [],
  menus: [],
  ...(groups ? { voicemailGroups: groups } : {}),
});

describe("voicemail group destination", () => {
  it("is one of the destination kinds", () => {
    expect(IVR_DESTINATION_KINDS).toContain("voicemail_group");
  });

  it("lists the groups by number and name", () => {
    const opts = targetOptions(
      "voicemail_group",
      choices([group("700", "Sales"), group("701", "Support")]),
      "",
    );
    expect(opts).toEqual([
      { value: "", label: "Choose…" },
      { value: "700", label: "700 · Sales" },
      { value: "701", label: "701 · Support" },
    ]);
  });

  it("keeps a stored group the list no longer has", () => {
    const opts = targetOptions("voicemail_group", choices([]), "799");
    expect(opts?.[1]).toEqual({ value: "799", label: "799 (not found)" });
  });

  it("offers only the choose entry where a page loads no groups", () => {
    expect(targetOptions("voicemail_group", choices(), "")).toHaveLength(1);
  });

  it("picks a group in the picker", () => {
    const onChange = vi.fn();
    render(
      <DestinationPicker
        id="d"
        label="Key 1"
        value={{ kind: "voicemail_group", target: "" }}
        choices={choices([group("700", "Sales")])}
        onChange={onChange}
      />,
    );
    fireEvent.change(screen.getByLabelText("Voicemail group"), {
      target: { value: "700" },
    });
    expect(onChange).toHaveBeenCalledWith({
      kind: "voicemail_group",
      target: "700",
    });
  });

  it("clears the target when the kind changes to a group", () => {
    const onChange = vi.fn();
    render(
      <DestinationPicker
        id="d"
        label="Key 1"
        value={{ kind: "extension", target: "101" }}
        choices={choices()}
        onChange={onChange}
      />,
    );
    fireEvent.change(screen.getByLabelText("Key 1"), {
      target: { value: "voicemail_group" },
    });
    expect(onChange).toHaveBeenCalledWith({
      kind: "voicemail_group",
      target: "",
    });
  });
});
