; Inno Setup script for the Windows installer. Built by scripts/package-windows.ps1:
;   iscc /DVersion=0.2.0 /DSource=build packaging\windows\banyan.iss

#ifndef Version
  #define Version "0.0.0"
#endif
#ifndef Source
  #define Source "..\..\build"
#endif

[Setup]
AppId={{6F0B4E5C-9C2A-4C55-9F43-2B7C1B4D8A10}
AppName=Banyan
AppVersion={#Version}
AppPublisher=Banyan
AppPublisherURL=https://github.com/Zenyet/banyan
DefaultDirName={autopf}\Banyan
DefaultGroupName=Banyan
DisableProgramGroupPage=yes
PrivilegesRequiredOverridesAllowed=dialog
OutputDir=..\..\dist
OutputBaseFilename=Banyan-{#Version}-windows-x64-setup
SetupIconFile=banyan.ico
UninstallDisplayIcon={app}\banyan.exe
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#Source}\banyan.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Banyan"; Filename: "{app}\banyan.exe"
Name: "{autodesktop}\Banyan"; Filename: "{app}\banyan.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\banyan.exe"; Description: "{cm:LaunchProgram,Banyan}"; Flags: nowait postinstall skipifsilent
