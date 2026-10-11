package api

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// uiPrefix is where the dashboard is mounted. Not the root: the API already
// owns paths like /devices and /jobs/{id}, and a single-page app at / would
// have to share them with routes its own router wants to use.
const uiPrefix = "/ui/"

// dashboardCSP keeps the dashboard to its own origin. The API key lives in the
// page's session storage, so script injection is the one thing that would turn
// a display bug into a stolen credential: no inline script, no third-party
// origin, no framing.
const dashboardCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// notBuiltPage answers /ui/ when the binary was compiled without a front-end
// build, instead of a bare 404 that reads like a routing bug.
const notBuiltPage = `<!doctype html><meta charset="utf-8"><title>Relaix</title>
<p>Le dashboard n'est pas inclus dans ce binaire. Lancez <code>make ui</code> puis
recompilez <code>gatewayd</code>, ou construisez l'image Docker, qui le fait.</p>`

// dashboard serves the single-page app out of fsys.
//
// Files that exist are served as they are. Anything else that looks like a
// client-side route — no file extension — gets index.html, so a reload on
// /ui/history lands on the app rather than a 404. A missing path that does
// have an extension stays a 404: answering a missing script with HTML would
// only trade a clear error for a confusing one.
func (s *Server) dashboard(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		index = nil
	}
	// The embedded files have no meaningful mtime; the process start stands in,
	// so a conditional request still revalidates after a redeploy.
	started := s.opts.Now()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", dashboardCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if index == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(notBuiltPage))
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), strings.TrimSuffix(uiPrefix, "/"))
		name = strings.TrimPrefix(name, "/")

		if name != "" && name != "index.html" {
			if f, err := fsys.Open(name); err == nil {
				stat, statErr := f.Stat()
				_ = f.Close()
				if statErr == nil && !stat.IsDir() {
					// Vite fingerprints everything under assets/, so a name
					// there never changes content and can be cached for good.
					if strings.HasPrefix(name, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					} else {
						w.Header().Set("Cache-Control", "no-cache")
					}
					http.ServeFileFS(w, r, fsys, name)
					return
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}

		// index.html names the current asset hashes, so it must be revalidated
		// on every load or a redeploy leaves browsers asking for old bundles.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", started.Truncate(time.Second), bytes.NewReader(index))
	})
}
