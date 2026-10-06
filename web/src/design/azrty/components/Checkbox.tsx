import type { InputHTMLAttributes, ReactNode } from "react";
import { Icon } from "./Icon";

/** Props for <Checkbox>. */
export interface CheckboxProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "type"
> {
  label: ReactNode;
  hint?: ReactNode;
}

/** The design system's checkbox (az-check with a box), for multi-choice rows. */
export function Checkbox({ label, hint, ...rest }: CheckboxProps) {
  return (
    <label className="az-check">
      <input type="checkbox" className="az-check__input" {...rest} />
      <span className="az-check__box">
        <Icon name="check" size={12} />
      </span>
      <span className="az-check__text">
        <span className="az-check__label">{label}</span>
        {hint && <span className="az-check__hint">{hint}</span>}
      </span>
    </label>
  );
}
