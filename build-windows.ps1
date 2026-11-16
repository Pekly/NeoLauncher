[CmdletBinding()]
param([ValidateSet('amd64','arm64')][string]$Architecture='amd64', [string]$OutputDirectory)
$ErrorActionPreference='Stop'
$go=(Get-Command go -ErrorAction Stop).Source
$version=& $go version
if($LASTEXITCODE -ne 0 -or $version -notmatch 'go(\d+)\.(\d+)' -or [int]$Matches[1] -lt 1 -or ([int]$Matches[1] -eq 1 -and [int]$Matches[2] -lt 26)){throw 'Go 1.26 or newer is required.'}
if(!$OutputDirectory){$OutputDirectory=Join-Path $PSScriptRoot "dist\windows-$Architecture"}
$OutputDirectory=[IO.Path]::GetFullPath($OutputDirectory)
$previous=@{GOOS=$env:GOOS;GOARCH=$env:GOARCH;CGO_ENABLED=$env:CGO_ENABLED;GOWORK=$env:GOWORK}
Push-Location $PSScriptRoot
try {
 $env:GOOS='windows';$env:GOARCH=$Architecture;$env:CGO_ENABLED='0';$env:GOWORK='off'
 New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
 $exe=Join-Path $OutputDirectory 'NeoLauncher.exe'
 & $go build -mod=readonly -trimpath -tags 'desktop,production' '-ldflags=-s -w -H windowsgui' -o $exe ./cmd/neolauncher
 if($LASTEXITCODE -ne 0){throw 'Build failed. Close the app if replacing its EXE.'}
 $readme=Join-Path $OutputDirectory 'README.md';$license=Join-Path $OutputDirectory 'LICENSE'
 Copy-Item (Join-Path $PSScriptRoot 'README.md') $readme -Force
 Copy-Item (Join-Path $PSScriptRoot 'LICENSE') $license -Force
 $files=@($exe,$readme,$license)
 $checksums=Join-Path $OutputDirectory 'SHA256SUMS.txt'
 [IO.File]::WriteAllLines($checksums,[string[]]@($files|ForEach-Object{'{0}  {1}' -f (Get-FileHash $_ -Algorithm SHA256).Hash.ToLowerInvariant(),[IO.Path]::GetFileName($_)}),[Text.Encoding]::ASCII)
 Compress-Archive -LiteralPath ($files+$checksums) -DestinationPath "$OutputDirectory.zip" -Force
 $hash=(Get-FileHash "$OutputDirectory.zip" -Algorithm SHA256).Hash.ToLowerInvariant()
 [IO.File]::WriteAllText("$OutputDirectory.zip.sha256","$hash  $([IO.Path]::GetFileName($OutputDirectory)).zip`r`n",[Text.Encoding]::ASCII)
 Write-Host "NeoLauncher ready: $exe"
 Write-Host 'Early scaffold: settings and server profiles work; game sign-in, installation, and launch are not implemented yet.'
} finally {
 $env:GOOS=$previous.GOOS;$env:GOARCH=$previous.GOARCH;$env:CGO_ENABLED=$previous.CGO_ENABLED;$env:GOWORK=$previous.GOWORK
 Pop-Location
}
