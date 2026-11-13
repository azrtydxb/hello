import { describe, expect, it } from "vitest";
import { authorizeUrl, newPkce, redirectUri } from "./oauth";

describe("HubSpot iframe sign-in", () => {
  it("makes an S256 challenge of its verifier and asks for the calls and events scopes of the first-party client", async () => {
    const p = await newPkce();
    const digest = await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(p.verifier),
    );
    expect(p.challenge).toBe(
      btoa(String.fromCharCode(...new Uint8Array(digest)))
        .replace(/\+/g, "-")
        .replace(/\//g, "_")
        .replace(/=+$/, ""),
    );
    const u = new URL(authorizeUrl("https://hello.example", p));
    expect(u.pathname).toBe("/oauth/authorize");
    expect(u.searchParams.get("client_id")).toBe("hello-hubspot");
    expect(u.searchParams.get("redirect_uri")).toBe(
      "https://hello.example/integrations/hubspot/callback",
    );
    expect(redirectUri("https://hello.example")).toBe(
      u.searchParams.get("redirect_uri"),
    );
    expect(u.searchParams.get("scope")).toBe("calls events");
    expect(u.searchParams.get("code_challenge_method")).toBe("S256");
    expect(u.searchParams.get("state")).toBe(p.state);
    expect((await newPkce()).state).not.toBe(p.state);
  });
});
