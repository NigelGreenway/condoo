# Condoo installer for Windows (Linux and macOS: install.sh).
#
#   .\install.ps1                 from a checkout: builds with Go if you have it, else downloads
#   & ([scriptblock]::Create((irm https://github.com/NigelGreenway/condoo/releases/latest/download/install.ps1)))
#   (options go after it: ... ))) -InstallDir D:\Tools\Condoo -Yes)
#
# Windows convention: programs installed per user live in %LOCALAPPDATA%\Programs, and that
# folder goes on your user PATH (no admin rights). It asks before changing PATH.
[CmdletBinding()]
param(
    [string]$InstallDir = "",
    [string]$Name = "",
    [string]$HerdrHome = $(if ($env:HERDR_APP_HOME) { $env:HERDR_APP_HOME } else { Join-Path $HOME ".config\herdr-app" }),
    [switch]$NoMod,
    [switch]$FromSource,
    [switch]$FromRelease,
    [string]$Version = "latest",
    [switch]$Yes,
    [switch]$Uninstall
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$Repo = if ($env:CONDOO_REPO) { $env:CONDOO_REPO } else { "NigelGreenway/condoo" }
$DefaultDir = Join-Path $env:LOCALAPPDATA "Programs\Condoo"

function Step($text) { Write-Host ""; Write-Host $text -ForegroundColor Cyan }
function Ask($question, $default) {
    if ($Yes -or -not [Environment]::UserInteractive) { return $default }
    $a = Read-Host "$question [$default]"
    if ([string]::IsNullOrWhiteSpace($a)) { return $default } else { return $a.Trim() }
}
function Confirm($question, $default) { return (Ask "$question (y/n)" $default) -match '^[Yy]' }
function ReleaseUrl($asset) {
    if ($Version -eq "latest") { "https://github.com/$Repo/releases/latest/download/$asset" }
    else { "https://github.com/$Repo/releases/download/$Version/$asset" }
}
function Fetch($asset, $file) { Invoke-WebRequest -UseBasicParsing -Uri (ReleaseUrl $asset) -OutFile $file }
function ExpectedHash($sums, $asset) {
    foreach ($line in Get-Content $sums) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $asset) { return $parts[0].ToLower() }
    }
    throw "checksums.txt doesn't list $asset"
}
function UserPath { [Environment]::GetEnvironmentVariable("Path", "User") }
function OnPath($dir) {
    $all = @((UserPath) -split ';') + @($env:Path -split ';')
    return $all | Where-Object { $_ -and ($_.TrimEnd('\') -ieq $dir.TrimEnd('\')) }
}

$SrcDir = if ($PSScriptRoot) { $PSScriptRoot } else { "" }
$InCheckout = $SrcDir -and (Test-Path (Join-Path $SrcDir "go.mod")) -and (Test-Path (Join-Path $SrcDir "cmd\condoo"))

# ---------- uninstall ----------
if ($Uninstall) {
    Step "Uninstalling Condoo"
    $dir = if ($InstallDir) { $InstallDir } else { Ask "Folder Condoo is installed in" $DefaultDir }
    $exe = Join-Path $dir "condoo.exe"
    $info = $null
    if (Test-Path $exe) {
        $info = & $exe config | ConvertFrom-Json
        Remove-Item $exe; Write-Host "Removed $exe"
    }
    if (OnPath $dir) {
        if (Confirm "Remove $dir from your user PATH?" "y") {
            $kept = ((UserPath) -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ine $dir.TrimEnd('\')) }) -join ';'
            [Environment]::SetEnvironmentVariable("Path", $kept, "User")
            Write-Host "Removed it from PATH."
        }
    }
    if ((Test-Path $dir) -and -not (Get-ChildItem $dir)) { Remove-Item $dir }
    $mod = Join-Path $HerdrHome "mods\condoo"
    if ((Test-Path $mod) -and (Confirm "Remove the Herdr panel ($mod)?" "y")) {
        Remove-Item -Recurse $mod; Write-Host "Removed the panel."
    }
    Write-Host ""
    Write-Host "Kept your list and settings; delete them yourself if you want them gone:"
    if ($info) { Write-Host "  database: $($info.db)"; Write-Host "  settings: $($info.config)" }
    exit 0
}

Write-Host "Condoo (Context + Todo): your agents' questions and requests, in one list."

# ---------- 1. the program ----------
$arch = switch ($env:PROCESSOR_ARCHITECTURE) { "AMD64" { "amd64" } "ARM64" { "arm64" } default { throw "unsupported CPU $env:PROCESSOR_ARCHITECTURE" } }
$haveGo = [bool](Get-Command go -ErrorAction SilentlyContinue)
$from = if ($FromSource) { "source" } elseif ($FromRelease) { "release" } elseif ($InCheckout -and $haveGo) { "source" } else { "release" }
if ($from -eq "source" -and -not $InCheckout) { throw "-FromSource needs this script in a checkout of the repo" }
if ($from -eq "source" -and -not $haveGo) { throw "-FromSource needs Go (https://go.dev/dl)" }

Step "1. Where Condoo goes"
Write-Host "Windows keeps per-user programs in $DefaultDir; that folder then goes on your user PATH."
$dir = if ($InstallDir) { $InstallDir } else { Ask "Install folder" $DefaultDir }
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir "condoo.exe"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("condoo-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Step "2. The condoo program"
    $new = Join-Path $tmp "condoo.exe"
    if ($from -eq "source") {
        $ver = (git -C $SrcDir describe --tags --always --dirty 2>$null); if (-not $ver) { $ver = "dev" }
        Write-Host "Building from $SrcDir"
        Push-Location $SrcDir
        try {
            $env:CGO_ENABLED = "0"
            go build -trimpath -ldflags "-s -w -X main.version=$ver" -o $new ./cmd/condoo
            if ($LASTEXITCODE -ne 0) { throw "go build failed" }
        } finally { Pop-Location }
    } else {
        $asset = "condoo-windows-$arch.exe"
        Write-Host "Downloading $asset ($Version) from github.com/$Repo"
        Fetch $asset $new
        Fetch "checksums.txt" (Join-Path $tmp "checksums.txt")
        $want = ExpectedHash (Join-Path $tmp "checksums.txt") $asset
        if ((Get-FileHash -Algorithm SHA256 $new).Hash.ToLower() -ne $want) { throw "checksum mismatch for ${asset}: not installing it" }
        Write-Host "Checksum verified."
    }
    $ours = $false
    if (Test-Path $exe) { try { $ours = ((& $exe version 2>$null) -join '') -match '"version"' } catch { } }
    if ((Test-Path $exe) -and -not $ours) {
        if (-not (Confirm "$exe exists and isn't Condoo. Replace it?" "n")) { throw "left $exe as it was" }
    }
    Copy-Item -Force $new $exe
    Write-Host "Installed $exe"

    Step "3. PATH"
    if (OnPath $dir) {
        Write-Host "$dir is on your PATH."
    } elseif (Confirm "Add $dir to your user PATH (no admin needed)?" "y") {
        $p = UserPath
        [Environment]::SetEnvironmentVariable("Path", $(if ($p) { "$p;$dir" } else { $dir }), "User")
        $env:Path = "$env:Path;$dir"
        Write-Host "Added. Open a new terminal, and restart Herdr, so they see it."
    } else {
        Write-Host "Not added. Agents need it on PATH to run condoo: add $dir yourself (Settings > Environment Variables)."
    }

    Step "4. Your name"
    Write-Host "Items agents raise are `"waiting on`" you. Leave it as your login name, or pick another."
    $cfg = & $exe config | ConvertFrom-Json
    if (-not $Name) { $Name = Ask "Name" $cfg.name }
    if ($Name -ne $cfg.name -or $cfg.name_source -eq "config") {
        & $exe config set name $Name | Out-Null
        Write-Host "Saved: $Name"
    } else {
        Write-Host "Using your login name ($($cfg.name)); change it any time: condoo config set name `"...`""
    }

    Step "5. The Herdr panel"
    $installedMod = $false
    $modDir = Join-Path $HerdrHome "mods\condoo"
    if ($NoMod) {
        Write-Host "Skipped (-NoMod)."
    } elseif (-not (Test-Path $HerdrHome)) {
        Write-Host "No Herdr folder at ${HerdrHome}: skipped. (-HerdrHome to point at it.)"
    } elseif (Confirm "Install the panel into $modDir?" "y") {
        if ($InCheckout) {
            $src = Join-Path $SrcDir "mods\condoo"
        } else {
            $tgz = Join-Path $tmp "condoo-mod.tar.gz"
            Fetch "condoo-mod.tar.gz" $tgz
            $want = ExpectedHash (Join-Path $tmp "checksums.txt") "condoo-mod.tar.gz"
            if ((Get-FileHash -Algorithm SHA256 $tgz).Hash.ToLower() -ne $want) { throw "checksum mismatch for the panel" }
            tar -xzf $tgz -C $tmp
            $src = Join-Path $tmp "mods\condoo"
        }
        New-Item -ItemType Directory -Force -Path $modDir | Out-Null
        # Only the mod's own files: Herdr's jsconfig.json / .d.ts and bin.json are left as they are.
        Get-ChildItem $src -File | Where-Object { ($_.Extension -eq ".js" -and $_.Name -notlike "*.d.ts") -or $_.Extension -eq ".svg" -or $_.Name -eq "mod.json" } |
            Copy-Item -Destination $modDir -Force
        @{ path = $exe } | ConvertTo-Json | Set-Content -Encoding utf8 (Join-Path $modDir "bin.json")
        if (Get-Command herdr-app -ErrorAction SilentlyContinue) { herdr-app check $modDir }
        $installedMod = $true
        Write-Host "Installed the panel."
    }
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Step "Done"
& $exe config
if ($installedMod) {
    Write-Host @'

Two things to add yourself (this script doesn't edit them):

1. A key for the panel, in the "global" section of your Herdr keys.json (pick any free key;
   note that editing keys.json stops Herdr updating its default keys there):

     "ctrl-shift-y": "mod.open:condoo"

   The key opens the panel as a dialog. To dock it as a sidebar instead, ask your agent in
   Herdr: "add a sidebar on the right for Condoo". Any layout block showing the condoo mod
   becomes the panel (shipped layouts are read-only, so it clones one first), e.g.:

     { "id": "condoo", "type": "block", "content": { "mod": "condoo" },
       "size": { "px": 340 }, "min": 260, "max": 640, "hidden": true, "background": "chrome" }

2. Tell your agents about it: copy AGENTS-snippet.md into your CLAUDE.md / AGENTS.md.
'@
}
$snippet = if ($InCheckout) { Join-Path $SrcDir "AGENTS-snippet.md" } else { ReleaseUrl "AGENTS-snippet.md" }
Write-Host "   It's at $snippet"
