param([string]$OutputDirectory = (Join-Path $PSScriptRoot '../dist/t07-fixture'))
$ErrorActionPreference = 'Stop'
$taskDestination = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $taskDestination | Out-Null
$taskPrevious = @{ GOOS=$env:GOOS; GOARCH=$env:GOARCH; CGO_ENABLED=$env:CGO_ENABLED }
try {
    $env:GOOS='linux'; $env:CGO_ENABLED='0'
    $taskArtifacts = @{}
    foreach ($taskArchitecture in @('amd64','arm64')) {
        $env:GOARCH=$taskArchitecture
        $taskName="filepanel-linux-$taskArchitecture"
        $taskOutput=Join-Path $taskDestination $taskName
        go build -trimpath -ldflags='-s -w' -o $taskOutput ./cmd/filepanel
        if ($LASTEXITCODE -ne 0) { throw '构建失败' }
        $taskArtifacts["linux-$taskArchitecture"] = @{file=$taskName;sha256=(Get-FileHash -LiteralPath $taskOutput -Algorithm SHA256).Hash.ToLowerInvariant();size=(Get-Item -LiteralPath $taskOutput).Length}
    }
    $taskManifest=Join-Path $taskDestination 'release.json'
    @{version='t07-local-fixture';artifacts=$taskArtifacts} | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $taskManifest -Encoding utf8NoBOM
    Write-Output "本地 fixture，非正式发布。清单 SHA256：$((Get-FileHash -LiteralPath $taskManifest).Hash.ToLowerInvariant())"
} finally {
    $env:GOOS=$taskPrevious.GOOS; $env:GOARCH=$taskPrevious.GOARCH; $env:CGO_ENABLED=$taskPrevious.CGO_ENABLED
}
