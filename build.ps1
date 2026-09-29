#!/usr/bin/env pwsh
<#
.SYNOPSIS
    QuestionMind 构建脚本（Windows / PowerShell 版本）。

.DESCRIPTION
    Makefile 需要 make 与类 Unix shell，Windows 上默认都没有。
    这个脚本提供同样的能力，让项目在主要目标平台上开箱即可构建。

    与 Makefile 保持一致的目标：web / build / desktop / api / test / check / cross / clean

.EXAMPLE
    .\build.ps1 build          # 前端 + 服务器版 + 桌面版
    .\build.ps1 desktop        # 只构建桌面版
    .\build.ps1 check          # 格式化 + vet + 全量测试
    .\build.ps1 -Task cross    # 交叉编译三平台 × 两种形态
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('web', 'build', 'desktop', 'api', 'test', 'check', 'cross', 'clean', 'run', 'help')]
    [string]$Task = 'help',

    # 跳过前端构建（只想出后端产物时用）
    [switch]$SkipWeb
)

$ErrorActionPreference = 'Stop'
$Root = $PSScriptRoot
$Backend = Join-Path $Root 'backend'
$Frontend = Join-Path $Root 'frontend'
$WebDist = Join-Path $Backend 'internal/web/dist'
$BuildDir = Join-Path $Backend 'build'

# ---- 版本信息（尽量与 Makefile 一致）----
function Get-VersionInfo {
    Push-Location $Root
    try {
        $version = (git describe --tags --always --dirty 2>$null)
        if (-not $version) { $version = 'dev' }
        $commit = (git rev-parse --short HEAD 2>$null)
        if (-not $commit) { $commit = 'unknown' }
    } finally { Pop-Location }
    $built = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    return @{ Version = $version; Commit = $commit; Built = $built }
}

function Get-Ldflags {
    $v = Get-VersionInfo
    $base = 'github.com/NanamiKyoka/questionmind/internal/version'
    return "-s -w -X $base.Version=$($v.Version) -X $base.Commit=$($v.Commit) -X $base.BuildTime=$($v.Built)"
}

function Invoke-Step([string]$Name, [scriptblock]$Action) {
    Write-Host "`n==> $Name" -ForegroundColor Cyan
    & $Action
    if ($LASTEXITCODE -ne 0 -and $null -ne $LASTEXITCODE) {
        throw "$Name 失败（退出码 $LASTEXITCODE）"
    }
}

function Test-Tool([string]$Name, [string]$Hint) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "找不到 $Name。$Hint"
    }
}

# ---- 目标实现 ----

function Build-Web {
    if ($SkipWeb) {
        Write-Host "==> 已指定 -SkipWeb，跳过前端构建" -ForegroundColor Yellow
        return
    }
    if (-not (Test-Path (Join-Path $Frontend 'package.json'))) {
        Write-Host "==> 未找到前端目录，跳过（产物将只含 API）" -ForegroundColor Yellow
        return
    }
    Test-Tool 'pnpm' '请先安装 pnpm：npm i -g pnpm'

    Push-Location $Frontend
    try {
        # 有 lockfile 时用 frozen 保证可复现；否则退化为普通安装。
        if (Test-Path 'pnpm-lock.yaml') {
            pnpm install --frozen-lockfile
        } else {
            pnpm install
        }
        pnpm build
    } finally { Pop-Location }

    # 先清空再复制：上次构建残留的旧哈希文件会被一起打进二进制，
    # 白白增大体积，还可能让浏览器加载到已不该存在的资源。
    New-Item -ItemType Directory -Force -Path $WebDist | Out-Null
    Get-ChildItem $WebDist -Force | Where-Object { $_.Name -ne '.gitkeep' } |
        Remove-Item -Recurse -Force
    Copy-Item (Join-Path $Frontend 'dist/*') $WebDist -Recurse -Force

    $files = Get-ChildItem $WebDist -Recurse -File
    $size = [Math]::Round(($files | Measure-Object -Property Length -Sum).Sum / 1KB, 1)
    Write-Host "==> 前端产物就绪：$($files.Count) 个文件 / $size KB" -ForegroundColor Green
}

function Build-Binary([string]$Cmd, [string]$OutFile) {
    Test-Tool 'go' '请先安装 Go 1.24+'
    $env:CGO_ENABLED = '0'
    $env:GOTOOLCHAIN = 'local'

    # 先删掉旧产物。
    #
    # 这一步不只是"清理"：如果构建失败而旧文件还在，脚本会去读旧文件的
    # 大小并打印出一个看起来正常的数字，把失败伪装成成功。
    # 这类"构建脚本骗人"的问题比构建失败本身危险得多。
    Remove-Item $OutFile -Force -ErrorAction SilentlyContinue

    $ldflags = Get-Ldflags

    Push-Location $Backend
    try {
        go build -trimpath -ldflags $ldflags -o $OutFile ".\cmd\$Cmd"
        # 紧跟 go build 检查退出码：中间插入任何 cmdlet 都不会重置它，
        # 但一旦中间又跑了别的原生命令，这里读到的就不是本次构建的结果了。
        if ($LASTEXITCODE -ne 0) {
            throw "go build .\cmd\$Cmd 失败（退出码 $LASTEXITCODE）"
        }
    } finally { Pop-Location }

    if (-not (Test-Path $OutFile)) {
        throw "go build 报告成功但没有产出 $OutFile"
    }
    $size = [Math]::Round((Get-Item $OutFile).Length / 1MB, 2)
    Write-Host "    $OutFile  ($size MB)" -ForegroundColor Green
}

function Invoke-Tests {
    Test-Tool 'go' '请先安装 Go 1.24+'
    $env:CGO_ENABLED = '0'
    $env:GOTOOLCHAIN = 'local'
    Push-Location $Backend
    try { go test ./... -count=1 } finally { Pop-Location }
}

function Invoke-Check {
    Test-Tool 'go' '请先安装 Go 1.24+'
    $env:CGO_ENABLED = '0'
    $env:GOTOOLCHAIN = 'local'
    Push-Location $Backend
    try {
        Write-Host "`n==> gofmt" -ForegroundColor Cyan
        $unformatted = gofmt -l .
        if ($unformatted) {
            Write-Host "以下文件未格式化：" -ForegroundColor Red
            $unformatted | ForEach-Object { Write-Host "  $_" }
            throw 'gofmt 检查未通过'
        }
        Write-Host "==> go vet" -ForegroundColor Cyan
        go vet ./...
        Write-Host "==> go test" -ForegroundColor Cyan
        go test ./... -count=1
    } finally { Pop-Location }
}

function Build-Cross {
    Test-Tool 'go' '请先安装 Go 1.24+'
    if (-not (Test-Path (Join-Path $WebDist 'index.html'))) {
        Write-Host "==> 未检测到已构建的前端，先执行 web 目标" -ForegroundColor Yellow
        Build-Web
    }
    New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null
    $env:CGO_ENABLED = '0'
    $env:GOTOOLCHAIN = 'local'
    $ldflags = Get-Ldflags

    # 纯 Go 让交叉编译不需要额外工具链，这是选型的直接收益。
    $targets = @(
        @{ OS = 'linux';   Arch = 'amd64'; Ext = '' },
        @{ OS = 'windows'; Arch = 'amd64'; Ext = '.exe' },
        @{ OS = 'darwin';  Arch = 'arm64'; Ext = '' }
    )
    Push-Location $Backend
    try {
        foreach ($t in $targets) {
            foreach ($cmd in @('server', 'desktop')) {
                $env:GOOS = $t.OS
                $env:GOARCH = $t.Arch
                $name = "qm-$cmd-$($t.OS)-$($t.Arch)$($t.Ext)"
                $out = Join-Path $BuildDir $name
                Remove-Item $out -Force -ErrorAction SilentlyContinue

                go build -trimpath -ldflags $ldflags -o $out ".\cmd\$cmd"
                if ($LASTEXITCODE -ne 0) {
                    throw "交叉编译 $name 失败（退出码 $LASTEXITCODE）"
                }
                if (-not (Test-Path $out)) {
                    throw "交叉编译报告成功但没有产出 $name"
                }
                $size = [Math]::Round((Get-Item $out).Length / 1MB, 2)
                Write-Host "    $name  ($size MB)" -ForegroundColor Green
            }
        }
    } finally {
        Remove-Item Env:GOOS -ErrorAction SilentlyContinue
        Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
        Pop-Location
    }
}

function Invoke-Clean {
    Remove-Item -Recurse -Force $BuildDir -ErrorAction SilentlyContinue
    Get-ChildItem $WebDist -Force -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -ne '.gitkeep' } | Remove-Item -Recurse -Force
    Write-Host "==> 已清理构建产物" -ForegroundColor Green
}

function Show-Help {
    @'
QuestionMind 构建脚本

用法:  .\build.ps1 <目标> [-SkipWeb]

目标:
  web       构建前端并复制到 embed 目录
  build     构建前端 + 服务器版 + 桌面版（默认入口）
  desktop   只构建桌面版
  api       只构建服务器版，不构建前端
  test      运行全量单元测试
  check     格式化检查 + vet + 测试（提交前跑）
  cross     交叉编译三平台 × 两种形态（共 6 份产物）
  clean     清理构建产物
  run       开发模式直接运行

选项:
  -SkipWeb  跳过前端构建

示例:
  .\build.ps1 build
  .\build.ps1 desktop -SkipWeb
  .\build.ps1 check
'@ | Write-Host
}

# ---- 分发 ----

Push-Location $Root
try {
    switch ($Task) {
        'web'     { Build-Web }
        'build'   {
            Build-Web
            New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null
            Invoke-Step '编译服务器版' { Build-Binary 'server' (Join-Path $BuildDir 'qm-server.exe') }
            Invoke-Step '编译桌面版'   { Build-Binary 'desktop' (Join-Path $BuildDir 'qm-desktop.exe') }
        }
        'desktop' {
            Build-Web
            New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null
            Invoke-Step '编译桌面版' { Build-Binary 'desktop' (Join-Path $BuildDir 'qm-desktop.exe') }
        }
        'api'     {
            New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null
            Invoke-Step '编译服务器版' { Build-Binary 'server' (Join-Path $BuildDir 'qm-server.exe') }
        }
        'test'    { Invoke-Tests }
        'check'   { Invoke-Check }
        'cross'   { Build-Cross }
        'clean'   { Invoke-Clean }
        'run'     {
            Test-Tool 'go' '请先安装 Go 1.24+'
            Push-Location $Backend
            try { go run ./cmd/server } finally { Pop-Location }
        }
        default   { Show-Help }
    }
} finally {
    Pop-Location
}
