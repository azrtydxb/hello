import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { json, mockApi } from "../test/api";
import { createPhoneLock } from "./lock";
import { CallButton } from "./CallButton";
import { SoftphoneDock } from "./SoftphoneDock";
import { SoftphoneProvider } from "./SoftphoneProvider";
import type {
  CallInfo,
  EngineEvent,
  EngineListener,
  PhoneEngine,
  Platform,
  RegisterConfig,
} from "./types";

class FakeEngine implements PhoneEngine {
  listener: EngineListener = () => undefined;
  log: string[] = [];
  registered: RegisterConfig | null = null;
  on(l: EngineListener) {
    this.listener = l;
    return () => undefined;
  }
  emit(ev: EngineEvent) {
    act(() => this.listener(ev));
  }
  register(cfg: RegisterConfig) {
    this.registered = cfg;
    this.log.push("register");
  }
  updateCredentials() {
    this.log.push("update");
  }
  unregister() {
    this.log.push("unregister");
  }
  call(t: string, o?: { video?: boolean }) {
    this.log.push(`call ${t}${o?.video ? " video" : ""}`);
    return "out1";
  }
  answer(id: string, o?: { video?: boolean }) {
    this.log.push(`answer ${id}${o?.video ? " video" : ""}`);
  }
  hangup(id: string) {
    this.log.push(`hangup ${id}`);
  }
  hold(id: string, on: boolean) {
    this.log.push(`hold ${id} ${on}`);
  }
  mute(id: string, on: boolean) {
    this.log.push(`mute ${id} ${on}`);
  }
  dtmf(id: string, t: string) {
    this.log.push(`dtmf ${id} ${t}`);
  }
  transfer(id: string, t: string) {
    this.log.push(`transfer ${id} ${t}`);
  }
  transferAttended(id: string, o: string) {
    this.log.push(`attended ${id} ${o}`);
  }
  setVideo(id: string, on: boolean) {
    this.log.push(`video ${id} ${on}`);
  }
  setDevices() {
    this.log.push("devices");
  }
  stats() {
    return Promise.resolve(null);
  }
  iceServers() {
    return [];
  }
  hasRelay() {
    return false;
  }
}

const incoming: CallInfo = {
  id: "c1",
  sipCallId: "sip-1",
  direction: "in",
  phase: "ringing",
  remote: "200",
  remoteName: "Ann Lee",
  called: "100",
  ringTag: "urgent",
  heldLocal: false,
  heldRemote: false,
  muted: false,
  video: { local: false, remote: false, offered: true },
  transferring: false,
  answeredAt: 0,
};

const platform = (mic: "ok" | "denied" = "ok"): Platform => ({
  getUserMedia: () =>
    mic === "ok"
      ? Promise.resolve({
          getTracks: () => [{ stop: () => undefined }],
        } as unknown as MediaStream)
      : Promise.reject(new DOMException("no", "NotAllowedError")),
  enumerateDevices: () => Promise.resolve([]),
  attachAudio: () => () => undefined,
  now: () => Date.now(),
});

const creds = {
  deviceId: 9,
  sipUsername: "w9.1",
  sipPassword: "s3cret",
  realm: "hello",
  wssUrl: "wss://hello.test/sip",
  expiresAt: new Date(Date.now() + 3600_000).toISOString(),
};
const routes = {
  "POST /api/v1/me/devices": () =>
    json(
      { ...creds, device: { id: 9, kind: "web" }, ice: { iceServers: [] } },
      201,
    ),
  "POST /api/v1/me/devices/9/credentials": () => json(creds),
  "GET /api/v1/me/ice-servers?deviceId=9": () =>
    json({
      iceServers: [{ urls: ["stun:s"] }],
      expiresAt: creds.expiresAt,
      ttlSeconds: 3600,
    }),
  "GET /api/v1/me/contacts?limit=200": () => json({ items: [], next: "" }),
  "GET /api/v1/me/calls?limit=50": () => json({ items: [], next: "" }),
};

function setup(
  opts: {
    mic?: "ok" | "denied";
    locks?: ReturnType<typeof createPhoneLock>;
  } = {},
) {
  const engine = new FakeEngine();
  const ringer = { start: vi.fn(), stop: vi.fn() };
  render(
    <MemoryRouter>
      <SoftphoneProvider
        displayName="u"
        engineFactory={() => engine}
        platform={platform(opts.mic)}
        lock={opts.locks ?? createPhoneLock(undefined)}
        ringer={ringer}
      >
        <SoftphoneDock />
      </SoftphoneProvider>
    </MemoryRouter>,
  );
  return { engine, ringer };
}

async function turnOn(engine: FakeEngine) {
  fireEvent.click(screen.getByRole("button", { name: /^Phone\./ }));
  fireEvent.click(
    screen.getByRole("switch", { name: /use this browser as a phone/i }),
  );
  await waitFor(() => expect(engine.registered).not.toBeNull());
  engine.emit({ type: "registration", state: "registered" });
}

beforeEach(() => {
  vi.stubGlobal("RTCPeerConnection", class {});
});
afterEach(() => vi.unstubAllGlobals());

describe("softphone dock", () => {
  it("is off until the switch is turned on, then enrols the browser and registers", async () => {
    const calls = mockApi(routes);
    const { engine } = setup();
    expect(
      screen.getByRole("button", { name: /Phone off/ }),
    ).toBeInTheDocument();
    await turnOn(engine);
    expect(
      calls.some((c) => c.method === "POST" && c.url === "/api/v1/me/devices"),
    ).toBe(true);
    expect(engine.registered).toMatchObject({
      sipUsername: "w9.1",
      wssUrl: "wss://hello.test/sip",
      iceServers: [{ urls: ["stun:s"] }],
    });
    expect(
      screen.getByRole("button", { name: /Registered/ }),
    ).toBeInTheDocument();
    // The next start refreshes the stored device instead of enrolling another.
    expect(
      JSON.parse(window.localStorage.getItem("hello.softphone") ?? "{}"),
    ).toMatchObject({ enabled: true, deviceId: 9 });
  });

  it("explains a blocked microphone and does not register", async () => {
    mockApi(routes);
    const { engine } = setup({ mic: "denied" });
    fireEvent.click(screen.getByRole("button", { name: /^Phone\./ }));
    fireEvent.click(
      screen.getByRole("switch", { name: /use this browser as a phone/i }),
    );
    expect(
      await screen.findByText(/blocked the microphone/i),
    ).toBeInTheDocument();
    expect(engine.registered).toBeNull();
  });

  it("shows the incoming call with its details, rings, and answers with or without video", async () => {
    mockApi(routes);
    const { engine, ringer } = setup();
    await turnOn(engine);
    engine.emit({ type: "call", call: incoming });
    const toast = await screen.findByRole("alertdialog", {
      name: /Incoming call from Ann Lee \(200\)/,
    });
    expect(toast).toHaveTextContent("Calling 100");
    expect(toast).toHaveTextContent("urgent");
    expect(toast).toHaveTextContent("Video");
    expect(ringer.start).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Answer with video" }));
    expect(engine.log).toContain("answer c1 video");
    engine.emit({
      type: "call",
      call: { ...incoming, phase: "active", answeredAt: Date.now() },
    });
    await waitFor(() => expect(ringer.stop).toHaveBeenCalled());
  });

  it("declines an incoming call and shows the missed call when the caller gives up", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    engine.emit({ type: "call", call: incoming });
    fireEvent.click(await screen.findByRole("button", { name: "Decline" }));
    expect(engine.log).toContain("hangup c1");
    engine.emit({
      type: "call",
      call: {
        ...incoming,
        phase: "ended",
        endReason: "answered-elsewhere",
        endText: "Call completed elsewhere",
      },
    });
    expect(
      await screen.findByText(/Call completed elsewhere/),
    ).toBeInTheDocument();
  });

  it("drives hold, mute, keypad and transfer on the active call", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    engine.emit({
      type: "call",
      call: { ...incoming, phase: "active", answeredAt: Date.now() },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Hold" }));
    fireEvent.click(screen.getByRole("button", { name: "Mute" }));
    fireEvent.click(screen.getByRole("button", { name: "Keypad" }));
    fireEvent.click(screen.getByRole("button", { name: "Key 5" }));
    fireEvent.click(screen.getByRole("button", { name: "Transfer" }));
    fireEvent.change(screen.getByLabelText("Transfer to"), {
      target: { value: "300" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Blind transfer" }));
    fireEvent.click(screen.getByRole("button", { name: "Start video" }));
    fireEvent.click(screen.getByRole("button", { name: "Hang up" }));
    expect(engine.log).toEqual(
      expect.arrayContaining([
        "hold c1 true",
        "mute c1 true",
        "dtmf c1 5",
        "transfer c1 300",
        "video c1 true",
        "hangup c1",
      ]),
    );
  });

  it("shows a recording badge with a camera mark when the PBX says the call is recorded with video", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    const active = {
      ...incoming,
      phase: "active" as const,
      answeredAt: Date.now(),
    };
    engine.emit({ type: "call", call: active });
    await screen.findByRole("button", { name: "Hold" });
    expect(screen.queryByText("REC")).toBeNull();
    engine.emit({ type: "call", call: { ...active, recording: "audio" } });
    expect(await screen.findByText("Recording")).toBeInTheDocument();
    expect(screen.getByTitle("This call is being recorded")).toBeVisible();
    engine.emit({ type: "call", call: { ...active, recording: "video" } });
    expect(
      await screen.findByText("Recording, including video"),
    ).toBeInTheDocument();
    expect(
      screen.getByTitle("This call is being recorded, video included"),
    ).toBeVisible();
    engine.emit({ type: "call", call: { ...active, recording: "off" } });
    await vi.waitFor(() => expect(screen.queryByText("REC")).toBeNull());
  });

  it("places a call from the dialer, with and without video", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    fireEvent.change(screen.getByLabelText("Number or name"), {
      target: { value: "201" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Video call" }));
    expect(engine.log).toContain("call 201 video");
    fireEvent.change(screen.getByLabelText("Number or name"), {
      target: { value: "202" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Call" }));
    expect(engine.log).toContain("call 202");
  });

  it("puts the first call on hold when a second is answered, and switches lines", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    engine.emit({
      type: "call",
      call: { ...incoming, phase: "active", answeredAt: 1 },
    });
    engine.emit({
      type: "call",
      call: { ...incoming, id: "c2", remote: "300", remoteName: "Bob" },
    });
    expect(await screen.findByText("Call waiting")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Answer$/ }));
    expect(engine.log).toEqual(
      expect.arrayContaining(["hold c1 true", "answer c2"]),
    );
  });

  it("shows the registration failure and the media failure in words", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    engine.emit({
      type: "call",
      call: { ...incoming, phase: "ended", endReason: "media-failed" },
    });
    expect(
      await screen.findByText(/lost its media connection.*No relay server/s),
    ).toBeInTheDocument();
    engine.emit({ type: "registration", state: "unregistered" });
    expect(
      await screen.findByText(/Reconnecting to the phone service/),
    ).toBeInTheDocument();
  });

  it("shows the read-only chip in a second tab and takes the phone on request", async () => {
    mockApi(routes);
    let free = false;
    const lock = createPhoneLock(undefined);
    const stub = {
      acquire: () => Promise.resolve(free),
      request: (g: () => void) => {
        free = true;
        g();
      },
      release: () => undefined,
      held: () => free,
    };
    void lock;
    const { engine } = setup({ locks: stub });
    fireEvent.click(screen.getByRole("button", { name: /^Phone\./ }));
    fireEvent.click(
      screen.getByRole("switch", { name: /use this browser as a phone/i }),
    );
    expect(
      await screen.findByText(/Phone active in another tab/),
    ).toBeInTheDocument();
    expect(engine.registered).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Use the phone here" }));
    await waitFor(() => expect(engine.registered).not.toBeNull());
  });

  it("answers with the keyboard shortcut", async () => {
    mockApi(routes);
    const { engine } = setup();
    await turnOn(engine);
    engine.emit({ type: "call", call: incoming });
    await screen.findByRole("alertdialog");
    fireEvent.keyDown(window, { key: "a", altKey: true });
    expect(engine.log).toContain("answer c1");
  });

  it("places a call from a Call button when the phone is on, and opens the dock to ask when it is off", async () => {
    mockApi(routes);
    const engine = new FakeEngine();
    render(
      <MemoryRouter>
        <SoftphoneProvider
          displayName="u"
          engineFactory={() => engine}
          platform={platform()}
          lock={createPhoneLock(undefined)}
          ringer={{ start: vi.fn(), stop: vi.fn() }}
        >
          <CallButton number="201" label="Bob" />
          <SoftphoneDock />
        </SoftphoneProvider>
      </MemoryRouter>,
    );
    const call = screen.getByRole("button", {
      name: "Call Bob from this browser",
    });
    fireEvent.click(call);
    expect(engine.log).not.toContain("call 201");
    expect(
      await screen.findByRole("region", { name: "Softphone" }),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("switch", { name: /use this browser as a phone/i }),
    );
    await waitFor(() => expect(engine.registered).not.toBeNull());
    engine.emit({ type: "registration", state: "registered" });
    fireEvent.click(call);
    expect(engine.log).toContain("call 201");
  });
});
