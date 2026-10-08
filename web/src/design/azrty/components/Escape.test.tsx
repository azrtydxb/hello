import { describe, expect, it, vi } from "vitest";
import { createRoot } from "react-dom/client";
import { Drawer } from "./Drawer";
import { Modal } from "./Modal";

// A dialog the user can see must already answer Escape. React flushes passive
// effects a task after the commit, so a window listener added in useEffect is
// missing for a moment while the dialog is on screen (and a test, or a fast
// typist, that presses Escape then loses the key). The listener goes in a
// layout effect, which runs inside the commit.
async function shownThenEscape(ui: React.ReactElement) {
  (
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = false;
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  const shown = new Promise<void>((resolve) => {
    const mo = new MutationObserver(() => {
      if (host.querySelector('[role="dialog"]')) {
        mo.disconnect();
        resolve();
      }
    });
    mo.observe(host, { childList: true, subtree: true });
  });
  root.render(ui);
  await shown;
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  root.unmount();
  host.remove();
  (
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
}

describe("Escape", () => {
  it("closes a Modal the moment it is on screen", async () => {
    const onClose = vi.fn();
    await shownThenEscape(<Modal title="T" onClose={onClose} />);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes a Drawer the moment it is on screen", async () => {
    const onClose = vi.fn();
    await shownThenEscape(<Drawer title="T" onClose={onClose} />);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
