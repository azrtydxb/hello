import { describe, expect, it } from "vitest";
import type { ClusterStatus, Trunk, TrunkStatus } from "../../api";
import {
  clusterIssues,
  destinationConfig,
  healthChecks,
  trunkIssues,
} from "./health";

const member = (id: string, state: string, kind = "sip") =>
  ({ id, kind, state, revisionLag: 0, configRevision: 5 }) as never;

const cluster = (over: Partial<ClusterStatus> = {}): ClusterStatus => ({
  members: [member("sip-1", "READY"), member("ctl-1", "READY", "control")],
  postgres: { up: true },
  valkey: { up: true, mode: "single" },
  configRevision: 5,
  ...over,
});

describe("platform health", () => {
  it("counts every node that is not READY and each dependency down", () => {
    expect(clusterIssues(cluster())).toBe(0);
    expect(
      clusterIssues(
        cluster({
          members: [member("sip-1", "DRAINING"), member("sip-2", "OFFLINE")],
          postgres: { up: false },
          valkey: { up: false, mode: "single" },
        }),
      ),
    ).toBe(4);
  });

  it("counts trunk destinations that are down", () => {
    const st = (ups: boolean[]): TrunkStatus =>
      ({
        trunkId: 1,
        name: "t",
        activeCalls: 0,
        destinations: ups.map((up, i) => ({
          destination: `d${i}`,
          up,
          checkedAt: "",
        })),
      }) as TrunkStatus;
    expect(trunkIssues([])).toBe(0);
    expect(trunkIssues([st([true, false]), st([false])])).toBe(2);
  });

  it("reports only checks it has data for", () => {
    expect(healthChecks({})).toEqual([]);
    const ids = healthChecks({ cluster: cluster() }).map((c) => c.id);
    expect(ids).toEqual(["sip", "control", "revision", "valkey", "postgres"]);
    expect(
      healthChecks({ cluster: cluster() }).every((c) => c.tone === "good"),
    ).toBe(true);
    const down = healthChecks({
      cluster: cluster({ members: [member("sip-1", "UNHEALTHY")] }),
    });
    expect(down.find((c) => c.id === "sip")).toMatchObject({
      tone: "bad",
      title: "SIP nodes ready: 0 of 1",
    });
  });

  it("matches a destination to its configured priority and weight", () => {
    const trunk = {
      destinations: [
        { host: "a.lab", port: 5070, priority: 2, weight: 3 },
        { host: "b.lab", port: 0, priority: 1, weight: 1 },
      ],
    } as Trunk;
    expect(destinationConfig(trunk, "a.lab:5070")).toMatchObject({
      priority: 2,
      weight: 3,
    });
    expect(destinationConfig(trunk, "b.lab:5060")).toMatchObject({
      priority: 1,
    });
    expect(destinationConfig(trunk, "c.lab:5060")).toBeUndefined();
    expect(destinationConfig(undefined, "a.lab:5070")).toBeUndefined();
  });
});
