import { ProductLogo } from "./design/azrty/components";

/**
 * Kuvryn Hello's lockup (Operate pillar). The design's emblem artwork is not
 * vendored yet, so ProductLogo shows the design system's dashed emblem
 * placeholder; pass `emblem`/`emblemLight` here once the PNGs are added.
 */
export const PRODUCT = {
  name: "Kuvryn",
  sub: "Hello",
  pillar: "operate",
  tagline: "AI IPBX for modern teams",
} as const;

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
    />
  );
}
