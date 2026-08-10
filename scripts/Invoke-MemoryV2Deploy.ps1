[CmdletBinding()]
param(
  [Parameter(Mandatory)][string]$ManifestPath,
  [Parameter(Mandatory)][string]$CandidatePath,
  [Parameter(Mandatory)][string]$ApprovedCandidateHash,
  [Parameter(Mandatory)][string]$LiveRoot,
  [Parameter(Mandatory)][string]$BackupRoot,
  [Parameter(Mandatory)][ValidateRange(1, 65535)][int]$Port,
  [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedLiveSchema,
  [Parameter(Mandatory)][ValidateRange(1, [int]::MaxValue)][int]$ExpectedTargetSchema,
  [string]$PythonPath = 'python'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

Import-Module (Join-Path $PSScriptRoot 'MemoryV2Deployment.psm1') -Force

Invoke-MemoryV2Deployment @PSBoundParameters
