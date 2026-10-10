' radar-tray-start.vbs
' Launches the Radar tray indicator at Windows logon (Startup folder).
' Runs radar-tray.ps1 with Windows PowerShell 5.1 in a fully hidden window (no console flash).
' Remove this file from the Startup folder to disable.

Set sh = CreateObject("WScript.Shell")
Dim script : script = sh.ExpandEnvironmentStrings("%USERPROFILE%") & "\.radar\radar-tray.ps1"
sh.Run "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File """ & script & """", 0, False
