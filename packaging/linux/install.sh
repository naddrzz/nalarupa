#!/bin/sh
# Install Nalarupa for the current user: ./install.sh   (remove: ./install.sh --uninstall)
# The app goes to ~/.local/lib/nalarupa, a `nalarupa` link to ~/.local/bin, plus a menu entry and icon under
# $XDG_DATA_HOME (default ~/.local/share).
set -e
here=$(cd "$(dirname "$0")" && pwd)
lib="$HOME/.local/lib/nalarupa"
bin="$HOME/.local/bin"
data="${XDG_DATA_HOME:-$HOME/.local/share}"
apps="$data/applications"
icons="$data/icons/hicolor/256x256/apps"

# Remove `path` only if it is what this installer creates there: a link to the app, or a regular file.
remove_ours() {
  if [ -L "$1" ]; then
    case "$(readlink "$1")" in "$lib"/*) rm -f "$1" ;; esac
  elif [ -f "$1" ]; then
    rm -f "$1"
  fi
}

if [ "$1" = "--uninstall" ]; then
  if [ -d "$lib" ]; then
    rm -f "$lib/nalarupa" "$lib/libnalarupa_core.so" "$lib"/gpuix-native.*.node
    rmdir "$lib" 2>/dev/null || true
  fi
  remove_ours "$bin/nalarupa"
  remove_ours "$apps/nalarupa.desktop"
  remove_ours "$icons/nalarupa.png"
  echo "Nalarupa removed (your documents and $data/Nalarupa stay)."
  exit 0
fi

# Never write over a directory, or a file this installer did not make, where its own files go.
for p in "$bin/nalarupa" "$apps/nalarupa.desktop" "$icons/nalarupa.png"; do
  if [ -d "$p" ] && [ ! -L "$p" ]; then
    echo "install.sh: $p is a directory; move it away and run again." >&2
    exit 1
  fi
done
if [ -e "$bin/nalarupa" ] && ! [ -L "$bin/nalarupa" ]; then
  echo "install.sh: $bin/nalarupa exists and is not this app's link; move it away and run again." >&2
  exit 1
fi

mkdir -p "$lib" "$bin" "$apps" "$icons"
# The binary loads the core and the GPUI addon from its own folder.
cp "$here/nalarupa" "$here/libnalarupa_core.so" "$here"/gpuix-native.*.node "$lib/"
ln -sfn "$lib/nalarupa" "$bin/nalarupa"
cp "$here/nalarupa.png" "$icons/nalarupa.png"
# Desktop Entry spec: quote the path and escape ", `, $ and \ inside the quotes (a home with spaces must still launch).
exec_path=$(printf '%s' "$lib/nalarupa" | sed 's/[\\"`$]/\\&/g')
sed "s|^Exec=.*|Exec=\"$(printf '%s' "$exec_path" | sed 's/[|&\\]/\\&/g')\"|" "$here/nalarupa.desktop" > "$apps/nalarupa.desktop"
echo "Nalarupa installed: run 'nalarupa' or open it from the app menu."
