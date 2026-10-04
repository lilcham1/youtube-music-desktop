; Per-user installer for Encore for YouTube Music.
; Build: makensis /DVERSION=0.3.0 installer\encore.nsi
; Replaces earlier installs named "YouTube Music" (the Go 0.2.x builds and
; the Electron 0.1.x releases, which shared one folder).

Unicode true
!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!define APP "Encore"
!define FULLNAME "Encore for YouTube Music"
!define EXE "Encore.exe"
!define UNINSTALLER "Uninstall Encore.exe"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Encore"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"

; Earlier installs, all in %LOCALAPPDATA%\Programs\YouTube Music.
!define OLD_APP "YouTube Music"
!define OLD_EXE "YouTube Music.exe"
!define OLD_UNINSTALLER "Uninstall YouTube Music.exe"
!define OLD_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\YouTubeMusicDesktop"
; electron-builder's per-user uninstall key for appId com.youtube.music.personal
; (read from an installed 0.1.33).
!define ELECTRON_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\4a3059be-5127-5ff0-bee1-14218944d62e"

Name "${FULLNAME}"
OutFile "..\dist\Encore-Setup-${VERSION}.exe"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
RequestExecutionLevel user
SetCompressor /SOLID lzma
BrandingText "${FULLNAME} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${FULLNAME}"
VIAddVersionKey "FileDescription" "${FULLNAME} Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Copyright (C) lilcham1. GPL-3.0. Not affiliated with Google or YouTube."

!define MUI_ICON "..\assets\icon.ico"
!define MUI_UNICON "..\assets\icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!insertmacro MUI_PAGE_LICENSE "..\LICENSE"
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

!define WEBVIEW2_KEY "Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

; The app runs on the Microsoft Edge WebView2 Runtime, which Windows 11
; includes. Offer Microsoft's download page if it is missing (interactive
; installs only; silent updates never prompt).
Function .onInit
  ; Machine-wide installs record their version in the 32-bit registry view
  ; (this installer's default view); per-user installs under HKCU.
  ReadRegStr $0 HKLM "SOFTWARE\${WEBVIEW2_KEY}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\${WEBVIEW2_KEY}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    IfSilent done
    MessageBox MB_YESNO|MB_ICONEXCLAMATION "${APP} needs the Microsoft Edge WebView2 Runtime, which isn't installed on this PC.$\n$\nOpen Microsoft's download page now? You can finish installing ${APP} either way; it will start once the runtime is installed." IDNO done
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/#download"
  ${EndIf}
  done:
FunctionEnd

; Ask a running copy to exit, then make sure it is gone so files can be replaced.
!macro StopApp
  nsExec::Exec 'taskkill /IM "${EXE}" /IM "${OLD_EXE}"'
  Sleep 1500
  nsExec::Exec 'taskkill /F /T /IM "${EXE}" /IM "${OLD_EXE}"'
  Sleep 500
!macroend

Section "Install"
  !insertmacro StopApp

  ; Replace an earlier "YouTube Music" install (Go 0.2.x or Electron 0.1.x).
  ; Their uninstallers only remove program files, shortcuts and registry
  ; entries; settings and the sign-in profile in %USERPROFILE%\.youtube-music
  ; stay and are used by this version.
  StrCpy $R0 "$LOCALAPPDATA\Programs\${OLD_APP}"
  ; Keep "Start with Windows" if the old version had it on.
  ; Only when its uninstall entry shows it is this app, never another
  ; product that happens to use that folder name.
  ReadRegStr $R2 HKCU "${OLD_KEY}" "UninstallString"
  ReadRegStr $R3 HKCU "${ELECTRON_KEY}" "UninstallString"
  ${If} $R2 != ""
  ${OrIf} $R3 != ""
    ReadRegStr $R1 HKCU "${RUN_KEY}" "${OLD_APP}"
    ${If} ${FileExists} "$R0\${OLD_UNINSTALLER}"
      ExecWait '"$R0\${OLD_UNINSTALLER}" /currentuser /S _?=$R0'
    ${EndIf}
    RMDir /r "$R0"
    Delete "$SMPROGRAMS\${OLD_APP}.lnk"
    Delete "$DESKTOP\${OLD_APP}.lnk"
    DeleteRegKey HKCU "${OLD_KEY}"
    DeleteRegKey HKCU "${ELECTRON_KEY}"
    DeleteRegValue HKCU "${RUN_KEY}" "${OLD_APP}"
    ${If} $R1 != ""
      WriteRegStr HKCU "${RUN_KEY}" "${APP}" '"$INSTDIR\${EXE}" --hidden'
    ${EndIf}
  ${EndIf}

  SetOutPath "$INSTDIR"
  File "..\dist\${EXE}"
  WriteUninstaller "$INSTDIR\${UNINSTALLER}"

  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE}"
  CreateShortcut "$DESKTOP\${APP}.lnk" "$INSTDIR\${EXE}"

  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${FULLNAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "lilcham1"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "URLInfoAbout" "https://lilcham1.github.io/youtube-music-desktop/"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\${UNINSTALLER}"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINSTALLER}" /S'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1

  ; ytm-desktop:// links (listen-along invites) open the app.
  WriteRegStr HKCU "Software\Classes\ytm-desktop" "" "URL:${FULLNAME}"
  WriteRegStr HKCU "Software\Classes\ytm-desktop" "URL Protocol" ""
  WriteRegStr HKCU "Software\Classes\ytm-desktop\DefaultIcon" "" '"$INSTDIR\${EXE}",0'
  WriteRegStr HKCU "Software\Classes\ytm-desktop\shell\open\command" "" '"$INSTDIR\${EXE}" "%1"'
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "EstimatedSize" $0

  ; In-app updates run "/S --run-after": start the new version when done.
  ${GetParameters} $0
  ClearErrors
  ${GetOptions} $0 "--run-after" $1
  IfErrors +2 0
    Exec '"$INSTDIR\${EXE}"'
SectionEnd

Section "Uninstall"
  !insertmacro StopApp
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\${UNINSTALLER}"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$DESKTOP\${APP}.lnk"
  DeleteRegValue HKCU "${RUN_KEY}" "${APP}"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\Classes\ytm-desktop"
  ; Settings and the sign-in profile in %USERPROFILE%\.youtube-music are kept.
SectionEnd
