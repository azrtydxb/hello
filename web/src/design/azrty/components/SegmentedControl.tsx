import { useState, type CSSProperties } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** One segmented option entry. */
export interface SegmentedOption<T extends string> {
  value: T;
  label: string;
  icon?: string;
}

/** Props for <SegmentedControl>. */
export interface SegmentedControlProps<T extends string> {
  options: ReadonlyArray<SegmentedOption<T>>;
  /** Controlled value; omit to let the control keep its own. */
  value?: T;
  defaultValue?: T;
  onChange?: (value: T) => void;
  mono?: boolean;
  block?: boolean;
  className?: string;
  style?: CSSProperties;
  "aria-label": string;
}

/** A radio group drawn as joined buttons (view toggles, time ranges). */
export function SegmentedControl<T extends string>({
  options,
  value,
  defaultValue,
  onChange,
  mono,
  block,
  className,
  style,
  "aria-label": ariaLabel,
}: SegmentedControlProps<T>) {
  const [inner, setInner] = useState<T | undefined>(
    defaultValue ?? options[0]?.value,
  );
  const cur = value ?? inner;
  return (
    <div
      className={cx(
        "az-seg",
        mono && "az-seg--mono",
        block && "az-seg--block",
        className,
      )}
      role="radiogroup"
      aria-label={ariaLabel}
      style={style}
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === cur}
          className={cx(
            "az-seg__item",
            o.value === cur && "az-seg__item--active",
          )}
          onClick={() => {
            setInner(o.value);
            onChange?.(o.value);
          }}
        >
          {o.icon && <Icon name={o.icon} size={14} />}
          {o.label}
        </button>
      ))}
    </div>
  );
}
