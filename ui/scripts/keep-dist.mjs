// Vite empties dist/ before every build, which deletes the placeholder that
// keeps `go:embed all:dist` compiling in a checkout with no front-end build.
// Putting it back keeps `git status` clean after `make ui`.
import { writeFileSync } from "node:fs";

writeFileSync(new URL("../dist/.gitkeep", import.meta.url), "");
