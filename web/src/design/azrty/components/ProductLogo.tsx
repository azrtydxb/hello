import type { CSSProperties } from "react";
import { cx } from "./cx";

/** The allowed pillar values. */
export type Pillar = "operate" | "build" | "solve";

/** Props for <ProductLogo>. */
export interface ProductLogoProps {
  name: string;
  /** Sub-name for two-word products ("Hello" in Kuvryn Hello). */
  sub?: string;
  /** Stacked layout only. */
  tagline?: string;
  pillar?: Pillar;
  /** Emblem image for dark surfaces; without one a dashed placeholder shows. */
  emblem?: string;
  /** Emblem image for light surfaces (defaults to `emblem`). */
  emblemLight?: string;
  layout?: "stacked" | "horizontal" | "icon";
  size?: number;
  className?: string;
  style?: CSSProperties;
}

/** A product lockup: emblem, wordmark, optional sub-name and tagline. */
export function ProductLogo({
  name,
  sub,
  tagline,
  pillar,
  emblem,
  emblemLight,
  layout = "stacked",
  size = 160,
  className,
  style,
}: ProductLogoProps) {
  const label = [name, sub].filter(Boolean).join(" ");
  return (
    <span
      className={cx("az-plogo", `az-plogo--${layout}`, className)}
      data-pillar={pillar}
      role="img"
      aria-label={label}
      style={{ "--az-plogo-size": `${size}px`, ...style } as CSSProperties}
    >
      {emblem ? (
        <>
          <img
            className="az-plogo__em az-plogo__em--dark"
            src={emblem}
            alt=""
          />
          <img
            className="az-plogo__em az-plogo__em--light"
            src={emblemLight || emblem}
            alt=""
          />
        </>
      ) : (
        <span className="az-plogo__ph">Emblem</span>
      )}
      {layout !== "icon" && (
        <span className="az-plogo__text">
          <span className="az-plogo__name">{name}</span>
          {sub && (
            <span className="az-plogo__sub">
              <span className="az-plogo__subtext">{sub}</span>
            </span>
          )}
          {tagline && layout === "stacked" && (
            <span className="az-plogo__tag">{tagline}</span>
          )}
        </span>
      )}
    </span>
  );
}
