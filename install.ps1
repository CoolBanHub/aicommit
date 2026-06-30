# aicommit — installer (Windows / PowerShell 5.1+)
#
# Quick start (public repo):
#   iwr -useb https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.ps1 | iex
#
# For a private repo, set a token with `repo` read access first:
#   $env:GITHUB_TOKEN = 'ghp_xxx'
#   iwr -useb https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.ps1 | iex
#
# Run as a file to pin a version / choose a destination:
#   .\install.ps1 v0.0.1 C:\bin

$ErrorActionPreference = 'Stop'

$Owner      = 'CoolBanHub'
$Repo       = 'aicommit'
$Version    = 'latest'
$InstallDir = ''

# Allow positional options when run as a file: ./install.ps1 [-v|--version] <tag> [-d|--dir] <path>
for ($i = 0; $i -lt $args.Length; $i++) {
  switch ($args[$i]) {
    '-v'        { $Version    = $args[++$i] }
    '--version' { $Version    = $args[++$i] }
    '-d'        { $InstallDir = $args[++$i] }
    '--dir'     { $InstallDir = $args[++$i] }
    default     { if (-not $Version) { $Version = $args[$i] } }
  }
}

$Token = $env:GITHUB_TOKEN
if (-not $Token) { $Token = $env:GH_TOKEN }

$headers = @{ 'User-Agent' = 'aicommit-installer' }
if ($Token) { $headers['Authorization'] = "Bearer $Token" }

if ([Environment]::Is64BitOperatingSystem) { $Arch = 'amd64' } else { $Arch = '386' }
$Asset = "aicommit-windows-$Arch.exe"

# Resolve the latest release via the GitHub API.
if ($Version -eq 'latest') {
  $rel = Invoke-RestMethod -Headers $headers "https://api.github.com/repos/$Owner/$Repo/releases/latest"
  $Version = $rel.tag_name
  if (-not $Version) { throw 'Could not resolve the latest release (private repo? set $env:GITHUB_TOKEN).' }
}

$Url = "https://github.com/$Owner/$Repo/releases/download/$Version/$Asset"

if (-not $InstallDir) { $InstallDir = Join-Path $env:USERPROFILE '.aicommit\bin' }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$Target = Join-Path $InstallDir 'aicommit.exe'
Write-Host "Downloading aicommit $Version ($Asset)..."
Invoke-WebRequest -Headers $headers -Uri $Url -OutFile $Target

Write-Host "Installed aicommit to $Target"

# Add the install directory to the user PATH if it is not there already.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$already = $false
if ($userPath) {
  foreach ($p in $userPath.Split(';')) { if ($p -eq $InstallDir) { $already = $true; break } }
}
if (-not $already) {
  $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Write-Host "Added $InstallDir to your user PATH. Open a new terminal, then run: aicommit commit --dry-run"
} else {
  Write-Host "Try: aicommit commit --dry-run"
}
