# Signs files with the code signing certificate from GitHub secrets.
# Needs WINDOWS_CERT_PFX_BASE64 and WINDOWS_CERT_PASSWORD in the environment.
param([Parameter(Mandatory=$true, ValueFromRemainingArguments=$true)][string[]]$Files)
$ErrorActionPreference = 'Stop'
$pfx = Join-Path $env:RUNNER_TEMP 'uley-cert.pfx'
[IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:WINDOWS_CERT_PFX_BASE64))
try {
  $signtool = Get-ChildItem 'C:\Program Files (x86)\Windows Kits\10\bin\*\x64\signtool.exe' |
    Sort-Object FullName -Descending | Select-Object -First 1
  & $signtool.FullName sign /f $pfx /p $env:WINDOWS_CERT_PASSWORD /fd SHA256 `
    /tr http://timestamp.digicert.com /td SHA256 /d "Uley" @Files
  if ($LASTEXITCODE -ne 0) { throw "signtool failed" }
} finally {
  Remove-Item $pfx -ErrorAction SilentlyContinue
}
