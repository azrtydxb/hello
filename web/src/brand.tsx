import { ProductLogo } from "./design/azrty/components";

// The Kuvryn Hello emblems, downscaled to 440px (the 220px stacked lockup
// at 2x) from the Claude Design export.
import EMBLEM_DARK from "./design/azrty/assets/kh-emblem-dark.webp";
import EMBLEM_LIGHT from "./design/azrty/assets/kh-emblem-light.webp";

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
