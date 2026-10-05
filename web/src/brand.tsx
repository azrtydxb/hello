import { ProductLogo } from "./design/azrty/components";

/**
 * The Kuvryn Hello emblems, when they are in the tree. The glob resolves to
 * nothing until design/azrty/assets/kh-emblem-{dark,light}.png exist, and
 * ProductLogo then shows the design system's dashed emblem placeholder, so
 * the build never depends on the artwork being there.
 */
const EMBLEMS = import.meta.glob<string>(
  "./design/azrty/assets/kh-emblem-*.png",
  { eager: true, query: "?url", import: "default" },
);
const EMBLEM_DARK = EMBLEMS["./design/azrty/assets/kh-emblem-dark.png"];
const EMBLEM_LIGHT =
  EMBLEMS["./design/azrty/assets/kh-emblem-light.png"] ?? EMBLEM_DARK;

/** Kuvryn Hello's product identity (Operate pillar). */
export const PRODUCT = {
  name: "Kuvryn",
  sub: "Hello",
  pillar: "operate",
  tagline: "AI IPBX for modern teams",
} as const;

/** The Kuvryn Hello lockup; the tagline shows in the stacked layout only. */
export function HelloLogo({
  layout,
  size,
}: {
  layout: "stacked" | "horizontal" | "icon";
  size: number;
}) {
  return (
    <ProductLogo
      name={PRODUCT.name}
      sub={PRODUCT.sub}
      pillar={PRODUCT.pillar}
      tagline={layout === "stacked" ? PRODUCT.tagline : undefined}
      layout={layout}
      size={size}
      emblem={EMBLEM_DARK}
      emblemLight={EMBLEM_LIGHT}
    />
  );
}
