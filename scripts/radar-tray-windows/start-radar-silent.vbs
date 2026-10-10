' start-radar-silent.vbs
' Silent launch of the Radar server at Windows logon (Startup folder).
' Runs start-radar-silent.ps1 in a fully hidden window (no console flash), no admin required.
' Remove this file from the Startup folder to disable.

Set sh = CreateObject("WScript.Shell")
Dim script : script = sh.ExpandEnvironmentStrings("%USERPROFILE%") & "\.radar\start-radar-silent.ps1"
sh.Run "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File """ & script & """", 0, False
