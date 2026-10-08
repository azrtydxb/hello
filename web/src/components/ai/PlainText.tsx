/**
 * Model and network text, shown as plain text with line breaks. React escapes
 * it: no HTML, no Markdown links or images, so model output cannot make the
 * browser fetch anything (spec S-5).
 */
export function PlainText({
  text,
  className,
}: {
  text: string;
  className?: string;
}) {
  return (
    <div className={className ? `aix-plain ${className}` : "aix-plain"}>
      {text}
    </div>
  );
}
