param(
    [Parameter(Mandatory = $true)]
    [string]$User,

    [Parameter(Mandatory = $true)]
    [string]$HostName,

    [Parameter(Mandatory = $true)]
    [string]$KeyPath,

    [string]$RemoteDir = "/opt/traefik-cloudflare-manager"
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command plink.exe -ErrorAction SilentlyContinue)) {
    throw "plink.exe was not found in PATH. Install PuTTY or add it to PATH."
}
if (-not (Get-Command pscp.exe -ErrorAction SilentlyContinue)) {
    throw "pscp.exe was not found in PATH. Install PuTTY or add it to PATH."
}
if (-not (Test-Path -LiteralPath $KeyPath)) {
    throw "Key file not found: $KeyPath"
}

$target = "$User@$HostName"
plink.exe -batch -i $KeyPath $target "mkdir -p $RemoteDir"
pscp.exe -batch -r -i $KeyPath ".\*" "${target}:$RemoteDir/"
plink.exe -batch -i $KeyPath $target "cd $RemoteDir && docker compose up -d --build"
