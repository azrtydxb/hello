export interface VersionInfo {
  version: string;
  commit: string;
  configRevision: number;
}

function isVersionInfo(value: unknown): value is VersionInfo {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.version === "string" &&
    typeof v.commit === "string" &&
    typeof v.configRevision === "number"
  );
}

export async function fetchVersion(signal?: AbortSignal): Promise<VersionInfo> {
  const res = await fetch("/api/v1/version", {
    headers: { Accept: "application/json" },
    signal,
  });
  if (!res.ok) {
    throw new Error(`GET /api/v1/version failed: HTTP ${res.status}`);
  }
  const body: unknown = await res.json();
  if (!isVersionInfo(body)) {
    throw new Error("GET /api/v1/version returned an unexpected payload");
  }
  return body;
}
