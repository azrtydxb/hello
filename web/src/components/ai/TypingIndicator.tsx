/**
 * The assistant is working: three pulsing dots and what it is doing now.
 * A polite live region, so a screen reader hears each new step (not each
 * animation frame); the dots stand still under prefers-reduced-motion.
 */
export function TypingIndicator({ label }: { label: string }) {
  return (
    <div className="aix-typing" role="status" aria-live="polite">
      <span className="aix-typing__dots" aria-hidden="true">
        <span />
        <span />
        <span />
      </span>
      <span className="aix-typing__label">{label}</span>
    </div>
  );
}
