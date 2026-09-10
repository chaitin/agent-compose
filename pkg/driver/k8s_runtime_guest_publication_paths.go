//go:build k8scompose

package driver

import "strings"

// Parent checks precede mkdir, tar, or rename. The canonical publication itself
// may be this mechanism's symlink, but a redirected parent must not send writes
// outside the private home or around the persistent-volume overlap checks.
func guestHomeParentGuardScript(home string, paths ...string) string {
	args := []string{shellQuote(home)}
	for _, path := range paths {
		args = append(args, shellQuote(path))
	}
	return "set -eu\ncommand -v node >/dev/null || { echo 'guest publication requires Node.js' >&2; exit 1; }\nnode -e " +
		shellQuote(guestHomeParentGuard) + " " + strings.Join(args, " ") + "\n"
}

const guestHomeParentGuard = `
const fs = require('node:fs');
const path = require('node:path');
const home = process.argv[1];
for (const destination of process.argv.slice(2)) {
  let current = home;
  const relative = path.relative(home, path.dirname(destination));
  if (relative === '..' || relative.startsWith('../') || path.isAbsolute(relative)) throw new Error('publication parent escapes guest home');
  for (const component of ['', ...relative.split('/').filter(Boolean)]) {
    if (component) current = path.join(current, component);
    let stat;
    try { stat = fs.lstatSync(current); }
    catch (error) { if (error.code === 'ENOENT') break; throw error; }
    if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error('publication parent is not a private directory: ' + current);
  }
}
`
