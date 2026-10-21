param(
    [switch]$ResetCredentials
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$composeProject = "snappy-finance-demo-local"
$overrides = @{
    DEMO_DATA = "true"
    API_PORT = "127.0.0.1:8081"
    PLAID_CLIENT_ID = ""
    PLAID_SECRET = ""
}
$previousEnvironment = @{}
foreach ($name in $overrides.Keys) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    [Environment]::SetEnvironmentVariable($name, $overrides[$name], "Process")
}
Push-Location $root

function New-RandomBase64([int]$ByteCount) {
    $bytes = New-Object byte[] $ByteCount
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($bytes)
    } finally {
        $rng.Dispose()
    }
    return [Convert]::ToBase64String($bytes)
}

try {
    $environmentFile = ".env.demo"
    $credentialsGenerated = $ResetCredentials -or -not (Test-Path $environmentFile)
    if ($credentialsGenerated) {
        $adminPassword = (New-RandomBase64 24).TrimEnd("=").Replace("+", "-").Replace("/", "_")
        $lines = @(
            "ADMIN_USERNAME=admin"
            "ADMIN_PASSWORD=$adminPassword"
            "SESSION_SECRET=$(New-RandomBase64 48)"
            "APP_ENCRYPTION_KEY=$(New-RandomBase64 32)"
            "POSTGRES_PASSWORD=$(New-RandomBase64 32)"
            "DEMO_DATA=true"
        )
        Set-Content -Path $environmentFile -Value $lines -Encoding ASCII
        Write-Host "Created $environmentFile with local-only credentials and demo data enabled."
        Write-Host "Sign-in username: admin"
        Write-Host "Sign-in password: $adminPassword"
    }

    if ($ResetCredentials) {
        docker compose --env-file $environmentFile -p $composeProject down --volumes
        if ($LASTEXITCODE -ne 0) {
            throw "Could not reset the demo Compose project (exit code $LASTEXITCODE)"
        }
        Write-Host "Removed the demo containers and database volume."
    } elseif (-not $credentialsGenerated) {
        Write-Host "Using the existing $environmentFile. To rotate credentials, run this script with -ResetCredentials."
    }

    docker compose --env-file $environmentFile -p $composeProject up --build
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose up failed with exit code $LASTEXITCODE"
    }
} finally {
    foreach ($name in $overrides.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], "Process")
    }
    Pop-Location
}
