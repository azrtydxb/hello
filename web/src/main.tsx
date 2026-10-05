import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { App } from "./App";
import { applyTheme, readStoredTheme } from "./design/azrty/theme";
import "./design/azrty/styles.css";
import "./index.css";
import "./app.css";

// Set the theme before the first paint; ThemeProvider keeps it in sync after.
applyTheme(readStoredTheme());

const root = document.getElementById("root");
if (!root) throw new Error("missing #root element");

createRoot(root).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
);
