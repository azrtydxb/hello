import { NavLink, Route, Routes } from "react-router";
import { NAV_ITEMS } from "./nav";
import { Dashboard } from "./pages/Dashboard";
import { NotFound } from "./pages/NotFound";
import { Placeholder } from "./pages/Placeholder";

export function App() {
  return (
    <div className="shell">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="brand">Hello</header>
      <nav className="sidebar" aria-label="Primary">
        <ul>
          {NAV_ITEMS.map((item) => (
            <li key={item.path}>
              {/* NavLink sets aria-current="page" on the active link. */}
              <NavLink to={item.path} end={item.path === "/"}>
                {item.label}
              </NavLink>
            </li>
          ))}
        </ul>
      </nav>
      <main id="main" className="content" tabIndex={-1}>
        <Routes>
          <Route index element={<Dashboard />} />
          {NAV_ITEMS.filter((item) => item.path !== "/").map((item) => (
            <Route
              key={item.path}
              path={item.path}
              element={<Placeholder title={item.label} phase={item.phase} />}
            />
          ))}
          <Route path="*" element={<NotFound />} />
        </Routes>
      </main>
    </div>
  );
}
