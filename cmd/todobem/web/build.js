'use strict';
// build.js — the page follows the server's build. Every answer names the UI the server was built
// with (X-Todobem-Build, a fingerprint of the embedded web tree); the first value seen is this
// page's, a different one later means the binary was replaced under the tab — the page reloads,
// its place kept in the hash, so a deploy is never watched through stale scripts. A restart of
// the same binary carries the same value and changes nothing, as does a server without the header.
let pageBuild = null;
// followBuild reads one answer; true means the page is reloading and the answer must not be
// used — the caller hands back a promise that never settles, so the old scripts never render
// a new build's data in the moment before the unload lands.
function followBuild(r) {
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
