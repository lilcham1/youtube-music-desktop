; Per-user installer for YouTube Music (Go/WebView2 build).
; Build: makensis /DVERSION=0.2.0 installer\youtube-music.nsi
; Installs to the same folder as the Electron releases and replaces them.

Unicode true
!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!define APP "YouTube Music"
!define EXE "YouTube Music.exe"
!define UNINSTALLER "Uninstall YouTube Music.exe"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\YouTubeMusicDesktop"
; electron-builder's per-user uninstall key for appId com.youtube.music.personal
; (read from an installed 0.1.33).
!define ELECTRON_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\4a3059be-5127-5ff0-bee1-14218944d62e"

Name "${APP}"
OutFile "..\dist\YouTube-Music-Setup-${VERSION}.exe"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
RequestExecutionLevel user
SetCompressor /SOLID lzma
BrandingText "${APP} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "FileDescription" "${APP} Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "Personal desktop wrapper for music.youtube.com"

!define MUI_ICON "..\assets\icon.ico"
!define MUI_UNICON "..\assets\icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
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
    MessageBox MB_YESNO|MB_ICONEXCLAMATION "YouTube Music needs the Microsoft Edge WebView2 Runtime, which isn't installed on this PC.$\n$\nOpen Microsoft's download page now? You can finish installing YouTube Music either way; it will start once the runtime is installed." IDNO done
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/#download"
  ${EndIf}
  done:
FunctionEnd

; Ask a running copy to exit, then make sure it is gone so files can be replaced.
!macro StopApp
  nsExec::Exec 'taskkill /IM "${EXE}"'
  Sleep 1500
  nsExec::Exec 'taskkill /F /T /IM "${EXE}"'
  Sleep 500
!macroend

Section "Install"
  !insertmacro StopApp

  ; Remove an Electron release in place. Its uninstaller only removes the
  ; program files; the shared profile in %USERPROFILE%\.youtube-music stays.
  IfFileExists "$INSTDIR\resources\app.asar" 0 +4
    ExecWait '"$INSTDIR\${UNINSTALLER}" /currentuser /S _?=$INSTDIR'
    RMDir /r "$INSTDIR"
    DeleteRegKey HKCU "${ELECTRON_KEY}"

  SetOutPath "$INSTDIR"
  File "..\dist\${EXE}"
  WriteUninstaller "$INSTDIR\${UNINSTALLER}"

  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE}"
  CreateShortcut "$DESKTOP\${APP}.lnk" "$INSTDIR\${EXE}"

  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "lilcham1"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\${UNINSTALLER}"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINSTALLER}" /S'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1

  ; ytm-desktop:// links (listen-along invites) open the app.
  WriteRegStr HKCU "Software\Classes\ytm-desktop" "" "URL:YouTube Music Desktop"
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
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APP}"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\Classes\ytm-desktop"
  ; Settings and the sign-in profile in %USERPROFILE%\.youtube-music are kept.
SectionEnd
