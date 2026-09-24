'use strict';
// build.js — the page follows the server's build. Every answer names the UI the server was built
// with (X-Todobem-Build, a fingerprint of the embedded web tree); the HTML response records its
// own value in a same-site cookie. A different API value means the binary was replaced under the
// tab — the page reloads, its place kept in the hash, so a deploy is never watched through stale
// scripts. A restart of the same binary carries the same value and changes nothing, as does a
// server without the header. The null fallback supports a page served by an older binary.
const pageBuildCookie = () => {
  try {
    const m = document.cookie.match(/(?:^|; )todobem-page-build=([^;]*)/);
    return m ? decodeURIComponent(m[1]) : null;
  } catch (e) {
    return null;
  }
};
let pageBuild = pageBuildCookie();
// serverVersion is the product version the server names on every answer (X-Todobem-Version:
// vX.Y.Z for a release, dev-<rev> for a build from a clone); null until one arrives, or from a
// server that predates the header.
let serverVersion = null;
// followBuild reads one answer; true means the page is reloading and the answer must not be
// used — the caller hands back a promise that never settles, so the old scripts never render
// a new build's data in the moment before the unload lands.
function followBuild(r) {
  const version = r.headers?.get('X-Todobem-Version');
  if (version) serverVersion = version;
  const build = r.headers?.get('X-Todobem-Build');
  if (!build) return false;
  if (pageBuild === null) {
    pageBuild = build;
    return false;
  }
  if (build === pageBuild) return false;
  location.reload();
  return true;
}
