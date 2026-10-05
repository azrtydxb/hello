import type { ReactNode } from "react";

/** The console page header: eyebrow, title, one-line description, actions. */
export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
}: {
  eyebrow: string;
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <header className="dir-head">
      <div>
        <span className="az-eyebrow dir-head__eyebrow">{eyebrow}</span>
        <h1 id="page-title" className="dir-head__title">
          {title}
        </h1>
        <p className="dir-head__desc">{description}</p>
      </div>
      {actions && <div className="dir-head__actions">{actions}</div>}
    </header>
  );
}
