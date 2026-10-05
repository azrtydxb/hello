import { useEffect, useState, type CSSProperties } from "react";
import { cx } from "./cx";
import { IconButton } from "./IconButton";

/** Props for <CodeBlock>. */
export interface CodeBlockProps {
  code: string;
  title?: string;
  copyable?: boolean;
  maxHeight?: number | string;
  className?: string;
  style?: CSSProperties;
}

/** Preformatted code with an optional title and copy button. */
export function CodeBlock({
  code,
  title,
  copyable = true,
  maxHeight,
  className,
  style,
}: CodeBlockProps) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1400);
    return () => clearTimeout(t);
  }, [copied]);
  const copy = () => {
    navigator.clipboard?.writeText(code).catch(() => {});
    setCopied(true);
  };
  return (
    <div className={cx("az-code", className)} style={style}>
      {(title || copyable) && (
        <div className="az-code__head">
          <span>{title}</span>
          {copyable && (
            <IconButton
              icon={copied ? "check" : "copy"}
              label={copied ? "Copied" : "Copy"}
              size={14}
              onClick={copy}
            />
          )}
        </div>
      )}
      <pre style={{ maxHeight }}>{code}</pre>
    </div>
  );
}
