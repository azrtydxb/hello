import { useState, type CSSProperties } from "react";
import { cx } from "./cx";

/** One tab item entry. */
export interface TabItem<T extends string> {
  id: T;
  label: string;
  count?: number;
}

/** Props for <Tabs>. */
export interface TabsProps<T extends string> {
  items: ReadonlyArray<TabItem<T>>;
  value?: T;
  defaultValue?: T;
  onChange?: (id: T) => void;
  "aria-label"?: string;
  /** When set, tab i gets id `${idPrefix}-tab-${id}` and controls `${idPrefix}-panel-${id}`. */
  idPrefix?: string;
  className?: string;
  style?: CSSProperties;
}

/** An underlined tab strip (role="tablist"). Render the panel yourself. */
export function Tabs<T extends string>({
  items,
  value,
  defaultValue,
  onChange,
  "aria-label": ariaLabel,
  idPrefix,
  className,
  style,
}: TabsProps<T>) {
  const [inner, setInner] = useState<T | undefined>(
    defaultValue ?? items[0]?.id,
  );
  const active = value ?? inner;
  return (
    <div
      className={cx("az-tabs", className)}
      role="tablist"
      aria-label={ariaLabel}
      style={style}
    >
      {items.map((it) => (
        <button
          key={it.id}
          type="button"
          role="tab"
          id={idPrefix ? `${idPrefix}-tab-${it.id}` : undefined}
          aria-controls={idPrefix ? `${idPrefix}-panel-${it.id}` : undefined}
          aria-selected={it.id === active}
          tabIndex={it.id === active ? 0 : -1}
          className={cx("az-tab", it.id === active && "az-tab--active")}
          onClick={() => {
            setInner(it.id);
            onChange?.(it.id);
          }}
        >
          {it.label}
          {it.count != null && (
            <span className="az-tab__count">{it.count}</span>
          )}
        </button>
      ))}
    </div>
  );
}
