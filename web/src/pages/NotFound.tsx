import { Link } from "react-router";

export function NotFound() {
  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Page not found</h1>
      <p>
        <Link to="/">Back to the dashboard</Link>
      </p>
    </section>
  );
}
