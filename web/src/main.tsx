import React from "react";
import ReactDOM from "react-dom/client";
import { HashRouter } from "react-router-dom";
import App, { isSelfQuotaPage } from "./App";
import "./styles.css";
import { initThemeSync } from "./store/themeSync";
import { initIsolatedLang, initLangSync } from "./i18n/langSync";

const selfQuotaPage = isSelfQuotaPage(window.location.pathname);
if (selfQuotaPage) {
  initIsolatedLang();
} else {
  // Mirror the host CPA panel's theme (light/white/dark) onto this iframe's
  // <html> before React mounts, so the first paint already matches the panel.
  initThemeSync();

  // Likewise mirror the panel's selected language (zh-CN/zh-TW/en/ru) into our
  // i18n store before React mounts.
  initLangSync();
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <HashRouter>
      <App />
    </HashRouter>
  </React.StrictMode>,
);
