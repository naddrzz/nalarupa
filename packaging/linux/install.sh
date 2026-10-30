#!/bin/sh
# Install Nalarupa for the current user: ./install.sh   (remove: ./install.sh --uninstall)
# The app goes to ~/.local/lib/nalarupa, a `nalarupa` command to ~/.local/bin, plus a menu entry and icon.
set -e
here=$(cd "$(dirname "$0")" && pwd)
lib="$HOME/.local/lib/nalarupa"
bin="$HOME/.local/bin"
apps="$HOME/.local/share/applications"
icons="$HOME/.local/share/icons/hicolor/256x256/apps"
if [ "$1" = "--uninstall" ]; then
  rm -rf "$lib" "$bin/nalarupa" "$apps/nalarupa.desktop" "$icons/nalarupa.png"
  echo "Nalarupa removed (your documents and ~/.local/share/Nalarupa stay)."
  exit 0
fi
mkdir -p "$lib" "$bin" "$apps" "$icons"
# The binary loads the core and the GPUI addon from its own folder.
cp "$here/nalarupa" "$here"/libnalarupa_core.so "$here"/*.node "$lib/"
ln -sf "$lib/nalarupa" "$bin/nalarupa"
cp "$here/nalarupa.png" "$icons/nalarupa.png"
sed "s|^Exec=.*|Exec=$lib/nalarupa|" "$here/nalarupa.desktop" > "$apps/nalarupa.desktop"
echo "Nalarupa installed: run 'nalarupa' or open it from the app menu."
