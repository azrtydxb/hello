import { Fragment, type CSSProperties, type ReactNode } from "react";
import { cx } from "./cx";

/** One property entry. */
export interface Property {
  label: ReactNode;
  value: ReactNode;
  /** Geist Mono value (identifiers). */
  mono?: boolean;
}

/** Props for <PropertyList>. */
export interface PropertyListProps {
  items: ReadonlyArray<Property>;
  className?: string;
  style?: CSSProperties;
}

/** Label/value pairs as a description list. */
export function PropertyList({ items, className, style }: PropertyListProps) {
  return (
    <dl className={cx("az-props", className)} style={style}>
      {items.map((it, i) => (
        <Fragment key={i}>
          <dt>{it.label}</dt>
          <dd className={it.mono ? "az-props__mono" : undefined}>{it.value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}
