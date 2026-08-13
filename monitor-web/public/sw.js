// Service Worker — Orbit
//
// No precache list any more. The old worker listed "/app.js" and "/style.css";
// the build now emits content-hashed files under /assets/, so a static list
// would either be wrong on the first deploy or need regenerating on every one.
// Instead everything is cached as it is fetched — the app shell lands in the
// cache on first visit, which is all the offline fallback ever did here.
const CACHE_NAME = "orbit-v9";

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (e) => {
    e.waitUntil(
        caches
            .keys()
            .then((keys) =>
                Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k)))
            )
            .then(() => self.clients.claim())
    );
});

self.addEventListener("fetch", (e) => {
    const url = new URL(e.request.url);

    // Live data and sockets are never cached.
    if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/ws/")) return;
    if (e.request.method !== "GET") return;

    // Network-first, cache fallback: the dashboard must show fresh metrics when
    // online and still open when the server is unreachable.
    e.respondWith(
        fetch(e.request)
            .then((res) => {
                const clone = res.clone();
                caches.open(CACHE_NAME).then((cache) => cache.put(e.request, clone));
                return res;
            })
            .catch(() => caches.match(e.request))
    );
});
