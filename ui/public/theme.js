// Applies the theme before first paint. A file rather than an inline <script>:
// the dashboard's CSP allows no inline script, and doing it later, in a React
// effect, flashes the light theme on every load. Mirrors src/hooks/use-theme.ts.
(function () {
  var t = null;
  try {
    t = localStorage.getItem("relaix.theme");
  } catch (e) {}
  if (t !== "light" && t !== "dark") {
    t = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  if (t === "dark") document.documentElement.classList.add("dark");
})();
