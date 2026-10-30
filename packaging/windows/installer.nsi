; Nalarupa installer. Built by .github/workflows/release.yml:
;   makensis /DVERSION=0.1.0 /DDIST=<app/dist> /DOUT=<setup.exe> packaging/windows/installer.nsi
; Installs per machine into Program Files, with Start menu and desktop shortcuts and an uninstaller.
Unicode true
!include "MUI2.nsh"

!ifndef VERSION
  !error "pass /DVERSION=x.y.z"
!endif
!ifndef DIST
  !error "pass /DDIST=<path to app/dist>"
!endif
!ifndef OUT
  !define OUT "Nalarupa-${VERSION}-windows-x64-setup.exe"
!endif

!define NAME "Nalarupa"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${NAME}"

Name "${NAME} ${VERSION}"
OutFile "${OUT}"
InstallDir "$PROGRAMFILES64\${NAME}"
InstallDirRegKey HKLM "Software\${NAME}" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma

; The version resource Explorer shows for setup.exe (needs four numeric parts).
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${NAME}"
VIAddVersionKey "FileDescription" "${NAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Copyright (c) 2026 naddrzz (MIT License)"

!define MUI_ICON "${DIST}\nalarupa.ico"
!define MUI_UNICON "${DIST}\nalarupa.ico"
!define MUI_ABORTWARNING
; "Run Nalarupa" goes through Explorer, so the editor does not inherit the installer's administrator rights.
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchUnelevated

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Function LaunchUnelevated
  Exec '"$WINDIR\explorer.exe" "$INSTDIR\nalarupa.exe"'
FunctionEnd

Section "Install"
  ; Per-machine install: shortcuts go to the all-users Start menu and desktop.
  SetShellVarContext all
  SetOutPath "$INSTDIR"
  ; The exe loads the core and the GPUI addon from its own folder (app/main.ts, app/src/core.ts).
  File "${DIST}\*.*"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortcut "$SMPROGRAMS\${NAME}.lnk" "$INSTDIR\nalarupa.exe" "" "$INSTDIR\nalarupa.ico"
  CreateShortcut "$DESKTOP\${NAME}.lnk" "$INSTDIR\nalarupa.exe" "" "$INSTDIR\nalarupa.ico"

  WriteRegStr HKLM "Software\${NAME}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${NAME}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\nalarupa.ico"
  WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext all
  ; Only what the installer wrote: the user's documents and app data (%LOCALAPPDATA%\Nalarupa) stay.
  ; Files still in use (Nalarupa open) are removed at the next restart.
  Delete /REBOOTOK "$INSTDIR\nalarupa.exe"
  ; By name: the folder is the user's choice, and other DLLs or addons in it are not ours.
  Delete /REBOOTOK "$INSTDIR\nalarupa_core.dll"
  Delete /REBOOTOK "$INSTDIR\gpuix-native.win32-x64-msvc.node"
  Delete /REBOOTOK "$INSTDIR\nalarupa.ico"
  Delete /REBOOTOK "$INSTDIR\uninstall.exe"
  RMDir /REBOOTOK "$INSTDIR"
  Delete "$SMPROGRAMS\${NAME}.lnk"
  Delete "$DESKTOP\${NAME}.lnk"
  DeleteRegKey HKLM "${UNINST_KEY}"
  DeleteRegKey HKLM "Software\${NAME}"
SectionEnd
