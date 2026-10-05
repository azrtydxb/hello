# Azrty design system in Hello

Hello's UI is built on the Azrty design system (Operate pillar, teal). This
folder holds a vendored copy of it plus typed React components that mirror the
design system's own components.

## Layout

| Path                                                | What                                                                                                                                 | Edit?         |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | ------------- |
| `styles.css`                                        | Entry point: imports every token file and `components/components.css`. Loaded once in `main.tsx`.                                    | No (vendored) |
| `tokens/*.css`                                      | `--az-*` tokens: colours, pillars, type, spacing, motion, icon font, fonts, base element styles.                                     | No (vendored) |
| `components/components.css`                         | Every `az-*` component class.                                                                                                        | No (vendored) |
| `components/hello.css`                              | Hello's additions for console patterns the bundle has no class for (`az-pagehead`, the paused LIVE dot). Tokens only.                | Yes           |
| `assets/fonts/*.woff2`, `assets/icons/lucide.woff2` | Self-hosted Geist, Geist Mono, Instrument Sans and the Lucide icon font. No CDN.                                                     | No (vendored) |
| `components/*.tsx`                                  | Typed React components, same markup and classes as the design system bundle (`_ds_bundle.js`, namespace `AzrtyDesignSystem_c1ca7a`). | Yes           |
| `components/index.ts`                               | The only import path for pages.                                                                                                      | Yes           |
| `theme.tsx`                                         | The theme switch (`ThemeProvider`, `useTheme`, `ThemeToggle`).                                                                       | Yes           |

The vendored files match the design system project
(`azrty-design-system-c1ca7a31-…`) except for Prettier formatting, which the
commit gate requires. To update them, copy the new files over, run them
through `procoder format`, and keep the `az-` prefix and `--az-` token names.

## Rules for pages

- **Pages use design components, no ad-hoc colours.** Import from
  `design/azrty/components`; style anything extra with `--az-*` tokens only
  (`var(--az-text-2)`, `var(--az-border)`, `var(--az-space-4)`, …). No hex,
  rgb or named colours, no new palettes.
- Status is never colour alone: a `Badge` or `Alert` always carries its label
  (and icon).
- Copy follows the design system voice: sentence case, verb-first buttons
  ("Add extension"), units on every number, "—" for unknown, no exclamation
  marks, no emoji.
- Identifiers (extensions, SIP URIs, hosts, commits) go in Geist Mono
  (`mono` on `Input`, `Table` columns, `PropertyList` items).
- Motion is the design system's: 120/180/260 ms, ease-out, and
  `prefers-reduced-motion` is respected globally. Do not add animations.
- `web/src/index.css` holds the few pre-design element defaults left, in
  `@layer legacy`; any `az-` class beats them. Delete a rule once nothing
  renders the bare element.
- A page builds no local copy of a component listed here. If two pages need
  the same piece, it moves into `components/` (matching the console design's
  markup), gets exported from `index.ts` and listed below.

## Components

`Alert`, `Avatar`, `Badge`, `Button`, `Checkbox`, `CodeBlock`,
`ConfirmDialog` (+ `useRestoreFocus`), `Drawer`, `EmptyState`, `Icon`,
`IconButton`, `Input`, `LineChart`, `LinkButton`, `LiveTag`, `Logo`, `Meter`,
`Modal`, `PageHeader`, `ProductLogo`, `PropertyList`, `SegmentedControl`,
`Select`, `Sidebar` (+ `SidebarNav`, `SidebarNavGroup`, `NavItemContent`,
`navItemClassName`), `Sparkline`, `Spinner`, `StatCard`, `Switch`, `Table`,
`Tabs`, `Toast` (+ `useToast`), `Topbar`.

```tsx
import { Alert, Button, Input, Table } from "../design/azrty/components";

<Input label="Extension" mono hint="2 to 10 digits." />
<Button icon="plus">Add extension</Button>
<Alert tone="bad" title="Could not save">{message}</Alert>   // role="alert"
<Table
  caption="Extensions"
  columns={[{ key: "number", label: "Number", mono: true }, { key: "name", label: "Name" }]}
  rows={extensions}
  rowKey={(e) => e.id}
/>
```

### Console patterns

Every page follows the same patterns, from `Kuvryn Hello Console.dc.html`:

| Pattern              | Use                                                                                                                                                                                                                                                    |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Page header          | `<PageHeader eyebrow="Directory" title="Extensions" description="…" actions={…} />`, the page's only `h1` (`id="page-title"`, so the page is `<section aria-labelledby="page-title">`). `back` replaces the eyebrow with a back link on a detail page. |
| Live views           | `<LiveTag every="5 s" live={state.status === "ready"} />` in the header actions; PAUSED when a refresh failed.                                                                                                                                         |
| Loading              | `<Spinner label="Loading trunks…" />` (role="status").                                                                                                                                                                                                 |
| Load failure         | `<Alert tone="bad" title="Could not load trunks">{message}</Alert>`; titles carry no full stop.                                                                                                                                                        |
| Empty list           | `<EmptyState icon="…" title="No trunks yet" description="…" action={…} />`; "No X match" when a filter hides everything.                                                                                                                               |
| Unknown value        | "—", never 0 or a blank. A badge says "Status unknown".                                                                                                                                                                                                |
| After a change       | One toast, a sentence with a full stop: `const toast = useToast(); toast.show("Trunk carrier-primary saved.")`, and render `{toast.node}` once in the page.                                                                                            |
| Destructive action   | `<ConfirmDialog title="Delete trunk X?" description="…" confirmLabel="Delete trunk" onConfirm={…} onClose={…} />`. Cancel has focus first; a rejection stays in the dialog. Draining a node keeps the design's in-place confirmation.                  |
| Link to another page | `<LinkButton to="/routes/test?from=101&number=112">`, never a button that navigates. The receiving page honours the query (see below).                                                                                                                 |

Query parameters pages honour: `/extensions?new=1` opens New extension;
`/routes/test?from=<extension or trunk:id>&number=` fills the tester and runs
it when both are given; `/routes?tab=inbound`; `/history?tab=failed`;
`/diagnostics?tab=trace&call=<cdr id>` and `?tab=reg&device=<id>`.

Icons are Lucide names (`<Icon name="phone-call" />`); the full list is in
`tokens/icons.css`. Decorative icons are `aria-hidden`; pass `label` when an
icon carries meaning on its own.

Accessibility the components handle: `Input`/`Select` tie the label and hint
to the control (`aria-describedby`), `Alert tone="bad"` is `role="alert"`,
`SegmentedControl` is a radio group, `Tabs` is a tablist (render the
`tabpanel` yourself; `idPrefix` wires `aria-controls`), `Modal`/`Drawer` are
labelled dialogs that close on Escape. Pass `aria-label` where a component
asks for one.

## Theming

One source of truth: `data-theme="dark" | "light"` on `<html>`, set by
`ThemeProvider` (in `App`) and before first paint in `main.tsx`. Dark is the
default; the viewer's choice is stored in `localStorage` under `hello.theme`
(wrapped in try/catch). The pillar is fixed: `data-pillar="operate"` on
`<html>`.

Use `useTheme()` to read or set it, `<ThemeToggle />` for the Dark/Light
control. Never read `prefers-color-scheme` in a page.

## Layout

The shell (`App.tsx`, styles in `web/src/app.css`) follows the Kuvryn Hello
console design (`Kuvryn Hello Console.dc.html`): a sticky 240px `Sidebar`
(product lockup, grouped nav from `web/src/nav.ts`, host and config revision,
user block with log out) and a 65px `Topbar` (breadcrumb "Kuvryn Hello ›
page", LIVE while the control plane answers, "Test a number", the theme
control). Pages render inside a container with the design's 32px 36px 60px
padding and 1650px max width. A page renders its own header and content only;
take its layout from the same console design file.

The product emblems load from `assets/kh-emblem-dark.png` and
`assets/kh-emblem-light.png` (see `web/src/brand.tsx`). Until those files are
added, `ProductLogo` shows the dashed placeholder; nothing else changes.
