# Hello PBX for Zapier

The private Zapier integration for Hello (spec integrations I-52). Setup, scopes
and behaviour are in [docs/zapier.md](../../docs/zapier.md).

```sh
npm install -g zapier-platform-cli
zapier login && zapier register "Hello PBX" && zapier push
node --test test/        # unit tests, no Zapier account or dependencies needed
```

Layout: `index.js` (authentication and middleware), `triggers/` (five REST-hook
triggers), `creates/place_call.js`, `searches/`, `lib/` (request helper and the
trigger factory).
