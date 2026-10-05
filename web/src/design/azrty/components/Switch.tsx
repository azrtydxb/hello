import type { CSSProperties, InputHTMLAttributes, ReactNode } from "react";
import { cx } from "./cx";

/** Props for <Switch>. */
export interface SwitchProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "type"
> {
  label?: ReactNode;
  hint?: ReactNode;
  labelPosition?: "start" | "end";
  className?: string;
  style?: CSSProperties;
}

/** An on/off switch: a checkbox with role="switch". */
export function Switch({
  label,
  hint,
  labelPosition = "start",
  disabled,
  className,
  style,
  ...rest
}: SwitchProps) {
  return (
    <label
      className={cx(
        "az-check",
        "az-check--switch",
        labelPosition === "end" && "az-check--end",
        disabled && "az-check--disabled",
        className,
      )}
      style={style}
    >
      <input
        type="checkbox"
        role="switch"
        className="az-check__input"
        disabled={disabled}
        {...rest}
      />
      <span className="az-switch__track" />
      {(label || hint) && (
        <span className="az-check__text">
          {label && <span className="az-check__label">{label}</span>}
          {hint && <span className="az-check__hint">{hint}</span>}
        </span>
      )}
    </label>
  );
}
