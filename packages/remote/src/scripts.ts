/**
 * The bash scripts the workspace runs on the remote machine. Each takes its inputs as
 * arguments; paths given to them were already made relative and free of `..` here, so
 * the scripts only have to make sure symbolic links do not lead out of the root.
 *
 * Exit codes: 3 a path outside the workspace, 4 the workspace root is gone, 2 a path that
 * is not what the call needs (a directory to read, a file to list), 5 a checkout problem,
 * 6 a file larger than the caller takes.
 */

/** What EXEC says when its directory is outside the workspace, so it is told from the command's own exit code 3. */
export const OUTSIDE = 'ariadne: outside the workspace'

/**
 * The first line EXEC writes to its error output, taken off again here: an exit code 255
 * without it means the connection failed, not the command (which may run ssh itself).
 */
export const STARTED = 'ariadne: started'

/** Sets `real_root`, and defines `resolve PATH`, which prints the real path inside it. */
const PRELUDE = `
set -u
cd -P -- "$1" 2>/dev/null || { printf '%s is missing on the remote machine\\n' "$1" >&2; exit 4; }
real_root=$(pwd -P)
shift
inside() { case "$1" in "$real_root" | "$real_root"/*) return 0 ;; esac; return 1; }
resolve() {
  local target rest="" real
  target=$real_root\${1:+/$1}
  while [ ! -e "$target" ] && [ "$target" != "$real_root" ]; do
    # A link that leads nowhere would be followed by a write.
    [ -L "$target" ] && return 3
    rest=/\${target##*/}$rest
    target=\${target%/*}
  done
  if [ -d "$target" ]; then
    real=$(cd -P -- "$target" && pwd -P) || return 3
  else
    real=$(realpath -- "$target" 2>/dev/null) || return 3
  fi
  inside "$real" || return 3
  printf '%s%s\\n' "$real" "$rest"
}
`

export const READ = `${PRELUDE}
path=$(resolve "$1") || exit 3
[ -d "$path" ] && { printf '%s is a directory\\n' "$1" >&2; exit 2; }
[ -e "$path" ] || { printf 'no such file: %s\\n' "$1" >&2; exit 2; }
exec cat -- "$path"
`

/** The file base64 encoded; exit 6 with its size on the error output when over `$2` bytes. */
export const READ_BYTES = `${PRELUDE}
path=$(resolve "$1") || exit 3
[ -d "$path" ] && { printf '%s is a directory\\n' "$1" >&2; exit 2; }
[ -e "$path" ] || { printf 'no such file: %s\\n' "$1" >&2; exit 2; }
size=$(wc -c < "$path") || exit 1
size=\${size//[!0-9]/}
[ "$size" -gt "$2" ] && { printf '%s\\n' "$size" >&2; exit 6; }
# At most one byte past the limit, so a file that grew meanwhile is still caught.
head -c "$(($2 + 1))" < "$path" | base64
`

export const WRITE = `${PRELUDE}
path=$(resolve "$1") || exit 3
[ -d "$path" ] && { printf '%s is a directory\\n' "$1" >&2; exit 2; }
mkdir -p -- "\${path%/*}" || exit 1
# The directories just made could not have been links; check the result once more.
path=$(resolve "$1") || exit 3
exec cat > "$path"
`

/** Prints `d name` or `f name` for each entry, NUL-separated; links count as files. */
export const LIST = `${PRELUDE}
path=$(resolve "$1") || exit 3
[ -d "$path" ] || { printf '%s is not a directory\\n' "$1" >&2; exit 2; }
cd -- "$path" || exit 1
for entry in * .[!.]* ..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  [ "$entry" = .git ] && continue
  if [ -d "$entry" ] && [ ! -L "$entry" ]; then printf 'd %s\\0' "$entry"; else printf 'f %s\\0' "$entry"; fi
done
`

/**
 * Run a thread's command in the directory `$1` with bash. It gets a process group of its
 * own and no input; a watcher reads the script's input, which the caller holds open, and
 * ends the group when it closes: on a timeout, a stop, or a dropped connection. Whatever
 * the command leaves running in the background ends with it.
 */
export const EXEC = `${PRELUDE}
printf '${STARTED}\\n' >&2
dir=$(resolve "$1") || { printf '${OUTSIDE}\\n' >&2; exit 3; }
[ -d "$dir" ] || { printf '%s is not a directory\\n' "$1" >&2; exit 2; }
cd -- "$dir" || exit 1
# With job control on, bash reports finished jobs on its own error output: keep that out
# of the command's.
exec 3>&2 2> /dev/null
set -m
bash -c "$2" < /dev/null 2>&3 3>&- &
child=$!
{ while read -r _; do :; done; kill -KILL -- "-$child"; } 3>&- &
watcher=$!
wait "$child"
code=$?
kill -KILL -- "-$child" "-$watcher"
exit "$code"
`

/**
 * What a source is on the remote machine: its real path, the git checkout it is part of
 * (empty when none) and the path inside it, and the user's home for worktrees.
 */
export const PROBE = `
set -u
cd -P -- "$1" 2>/dev/null || { printf '%s is not a directory on the remote machine\\n' "$1" >&2; exit 2; }
real=$(pwd -P)
top=$(git rev-parse --show-toplevel 2>/dev/null || true)
sub=""
if [ -n "$top" ]; then
  top=$(cd -P -- "$top" && pwd -P)
  case "$real" in "$top"/*) sub=\${real#"$top"/} ;; esac
fi
printf '%s\\0%s\\0%s\\0%s\\0' "$real" "$top" "$sub" "$HOME"
`

/**
 * Give a thread its worktree: `$1` the repository, `$2` the worktree directory, `$3` its
 * branch, `$4` the ref a new branch starts from, `$5` the path inside the repository the
 * workspace is. The server's git there runs no hooks and no fsmonitor. A new worktree gets
 * the checkout's ignored dependency directories as copy-on-write clones when the
 * filesystem clones (`cp --reflink=always`, or `cp -c` on macOS); otherwise nothing.
 * Prints the real paths the workspace keeps: the root, then the git directory.
 */
export const OPEN_WORKTREE = `
set -u
repo=$1 dir=$2 branch=$3 start=$4 sub=$5
export GIT_TERMINAL_PROMPT=0
g() { git -c core.hooksPath=/dev/null -c core.fsmonitor=false -C "$repo" "$@"; }
clone() {
  if [ "$(uname)" = Darwin ]; then cp -c -R -- "$1" "$2"; else cp -a --reflink=always -- "$1" "$2"; fi
}
seed() {
  local common probe entry
  common=$(g rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || return 0
  probe=ariadne-clone-probe-$$-$RANDOM
  printf probe > "$common/$probe" || return 0
  if ! clone "$common/$probe" "$dir/.$probe" 2> /dev/null; then
    rm -f -- "$common/$probe" "$dir/.$probe"
    return 0
  fi
  rm -f -- "$common/$probe" "$dir/.$probe"
  g ls-files --others --ignored --exclude-standard --directory -z | while IFS= read -r -d '' entry; do
    entry=\${entry%/}
    case "\${entry##*/}" in node_modules | vendor | .pnpm-store) ;; *) continue ;; esac
    [ -e "$dir/$entry" ] && continue
    # A link would be copied as the link, and installing would write through it.
    { [ -d "$repo/$entry" ] && [ ! -L "$repo/$entry" ]; } || continue
    mkdir -p -- "$(dirname -- "$dir/$entry")"
    clone "$repo/$entry" "$dir/$entry" 2> /dev/null || rm -rf -- "$dir/$entry"
  done
}
if [ ! -e "$dir/.git" ]; then
  mkdir -p -- "$(dirname -- "$dir")" || exit 5
  # A worktree whose directory was deleted by hand is still registered; forget it first.
  g worktree prune
  if g rev-parse --verify --quiet "refs/heads/$branch" > /dev/null; then
    g worktree add "$dir" "$branch" >&2 || exit 5
  else
    g worktree add -b "$branch" "$dir" "$start" >&2 || exit 5
  fi
  seed
fi
root=$dir
if [ -n "$sub" ]; then
  root=$dir/$sub
  [ -d "$root" ] || {
    printf '%s is not in the repository'"'"'s current commit (untracked or ignored), so the thread'"'"'s checkout has no such directory; commit it, or choose the repository root\\n' "$sub" >&2
    exit 5
  }
fi
printf '%s\\0' "$(cd -P -- "$root" && pwd -P)"
`

/**
 * git in the directory `$1` (the repository, or a worktree of it), with the rest as its
 * arguments. Like the server's other git on the remote machine, it runs no hooks and no
 * fsmonitor.
 */
export const GIT = `
set -u
cd -P -- "$1" 2>/dev/null || { printf '%s is missing on the remote machine\\n' "$1" >&2; exit 4; }
shift
export GIT_TERMINAL_PROMPT=0
exec git -c core.hooksPath=/dev/null -c core.fsmonitor=false "$@"
`

/** Exits 0 when the path `$1` exists. */
export const EXISTS = `[ -e "$1" ] || [ -L "$1" ]`

/** The directories in `$1`, each as its name, its real path and its size in KiB, NUL-separated. */
export const WORKTREES = `
set -u
cd -P -- "$1" 2>/dev/null || exit 0
for entry in */; do
  [ -d "$entry" ] && [ ! -L "\${entry%/}" ] || continue
  name=\${entry%/}
  size=$(du -sk -- "$name" 2>/dev/null | cut -f1)
  printf '%s\\0%s\\0%s\\0' "$name" "$(pwd -P)/$name" "\${size:-}"
done
`
