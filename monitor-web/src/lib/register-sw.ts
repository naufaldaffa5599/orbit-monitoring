/**
 * Registers the service worker that makes this installable as a PWA.
 *
 * Old registrations are unregistered first: this app shipped for months from
 * monitor-app/ with a cache-first worker whose precache list points at files
 * (app.js, style.css) that no longer exist. A phone with that worker installed
 * would keep serving the old dashboard from cache forever otherwise.
 */
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    void navigator.serviceWorker
      .getRegistrations()
      .then((regs) => Promise.all(regs.map((reg) => reg.unregister())))
      .then(() => navigator.serviceWorker.register("/sw.js?v=9"))
      .catch(() => {
        /* insecure context or unsupported — the app works without it */
      })
  })
}
