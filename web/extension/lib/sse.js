// A server-sent event parser for fetch bodies (EventSource cannot send the
// Authorization header). Feed it text; it returns the events completed so far.

export function createSseParser() {
  let buf = "";
  return {
    /** Adds a chunk; returns [{id, data}] for every completed event. keepalive comments are dropped. */
    push(chunk) {
      buf += chunk.replace(/\r\n?/g, "\n");
      const out = [];
      for (let i = buf.indexOf("\n\n"); i >= 0; i = buf.indexOf("\n\n")) {
        const block = buf.slice(0, i);
        buf = buf.slice(i + 2);
        let id = "";
        const data = [];
        for (const line of block.split("\n")) {
          if (line.startsWith(":")) continue;
          const c = line.indexOf(":");
          const field = c < 0 ? line : line.slice(0, c);
          const val = c < 0 ? "" : line.slice(c + 1).replace(/^ /, "");
          if (field === "id") id = val;
          else if (field === "data") data.push(val);
        }
        if (data.length > 0) out.push({ id, data: data.join("\n") });
      }
      return out;
    },
  };
}
