import type { CSSProperties, ReactNode, SelectHTMLAttributes } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";
import { fieldId } from "./Input";

/** One select option entry. */
export interface SelectOption {
  value: string;
  label: string;
}

/** Props for <Select>. */
export interface SelectProps extends Omit<
  SelectHTMLAttributes<HTMLSelectElement>,
  "size"
> {
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  options: ReadonlyArray<string | SelectOption>;
  size?: "sm" | "md";
  id?: string;
  className?: string;
  style?: CSSProperties;
}

/** A labelled native select with the design system chevron. */
export function Select({
  label,
  hint,
  error,
  options,
  size,
  id,
  className,
  style,
  ...rest
}: SelectProps) {
  const iid = fieldId(id, label);
  const hintId = iid && (error || hint) ? `${iid}-hint` : undefined;
  const opts = options.map((o) =>
    typeof o === "string" ? { value: o, label: o } : o,
  );
  return (
    <div
      className={cx("az-field", Boolean(error) && "az-field--error", className)}
      style={style}
    >
      {label && (
        <label className="az-field__label" htmlFor={iid}>
          {label}
        </label>
      )}
      <span className="az-input-wrap az-select-wrap">
        <select
          id={iid}
          className={cx(
            "az-input",
            "az-select",
            size === "sm" && "az-input--sm",
          )}
          aria-invalid={error ? true : undefined}
          aria-describedby={hintId}
          {...rest}
        >
          {opts.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <Icon name="chevron-down" size={15} className="az-select__chev" />
      </span>
      {(error || hint) && (
        <span className="az-field__hint" id={hintId}>
          {error || hint}
        </span>
      )}
    </div>
  );
}
