Set-StrictMode -Version Latest

if (-not ('AgentDCAppPackageByteScanner' -as [type])) {
  Add-Type -TypeDefinition @'
using System;
using System.IO;

public static class AgentDCAppPackageByteScanner
{
    private const int BufferSize = 64 * 1024;

    public static bool ContainsAny(string path, byte[][] needles)
    {
        if (needles == null || needles.Length == 0) return false;
        int maxNeedle = 0;
        foreach (byte[] needle in needles)
        {
            if (needle == null || needle.Length == 0) throw new ArgumentException("empty needle");
            if (needle.Length > maxNeedle) maxNeedle = needle.Length;
        }

        byte[] buffer = new byte[BufferSize + maxNeedle - 1];
        int carry = 0;
        using (FileStream stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read))
        {
            int read;
            while ((read = stream.Read(buffer, carry, BufferSize)) > 0)
            {
                int total = carry + read;
                foreach (byte[] needle in needles)
                {
                    int last = total - needle.Length;
                    for (int offset = 0; offset <= last; offset++)
                    {
                        if (buffer[offset] != needle[0]) continue;
                        int index = 1;
                        while (index < needle.Length && buffer[offset + index] == needle[index]) index++;
                        if (index == needle.Length) return true;
                    }
                }

                carry = Math.Min(maxNeedle - 1, total);
                if (carry > 0) Buffer.BlockCopy(buffer, total - carry, buffer, 0, carry);
            }
        }
        return false;
    }
}
'@
}

function Resolve-BuildPaths {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Repo,
    [Parameter(Mandatory)][string]$Out
  )

  $repoPath = [IO.Path]::GetFullPath($Repo)
  $outPath = [IO.Path]::GetFullPath($Out)

  if (-not (Test-Path -LiteralPath (Join-Path $repoPath '.git'))) {
    throw "không thấy repo Git ở '$repoPath'"
  }

  $repoKey = $repoPath.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
  $outKey = $outPath.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
  $repoPrefix = $repoKey + [IO.Path]::DirectorySeparatorChar
  $outPrefix = $outKey + [IO.Path]::DirectorySeparatorChar

  if ($repoKey.Equals($outKey, [StringComparison]::OrdinalIgnoreCase) -or
      $repoKey.StartsWith($outPrefix, [StringComparison]::OrdinalIgnoreCase) -or
      $outKey.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "repo và output không được trùng hoặc nằm bên trong nhau: '$repoPath', '$outPath'"
  }

  [PSCustomObject]@{
    Repo = $repoPath
    Out = $outPath
  }
}

function Assert-CleanGitSource {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Repo)

  $repoPath = [IO.Path]::GetFullPath($Repo)
  $status = (& git -C $repoPath --no-optional-locks status --porcelain=v1 --untracked-files=all) -join "`n"
  if ($LASTEXITCODE -ne 0) {
    throw "không đọc được Git status của '$repoPath'"
  }
  if (-not [string]::IsNullOrWhiteSpace($status)) {
    throw "repo nguồn không sạch; commit hoặc cất thay đổi trước khi build: '$repoPath'`n$status"
  }

  return $repoPath
}

function Get-AppGoTestSkipPattern {
  [CmdletBinding()]
  param()

  $names = @(
    'TestAppJSKnowsTheSessionEndedCloseReason'
    'TestAppJSSendsThePortalMutationHeader'
    'TestPortalReloadedKeyWithLiveSessionReachesTheShell'
    'TestPortalRootServesTheShellWithACookie'
    'TestPortalUsesModalNotBrowserDialogs'
    'TestAgentPortalNoLongerCarriesZalo'
    'TestModalCallsPassAnObject'
  )
  $escaped = $names | ForEach-Object { [regex]::Escape($_) }
  return '^(?:' + ($escaped -join '|') + ')$'
}

function Clear-AppOutput {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Out,
    [switch]$KeepData
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  $volumeRoot = [IO.Path]::GetPathRoot($outPath)
  if ($outPath.TrimEnd('\', '/').Equals($volumeRoot.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase)) {
    throw "từ chối xóa nội dung thư mục gốc '$outPath'"
  }
  if (-not (Test-Path -LiteralPath $outPath)) { return }

  $items = Get-ChildItem -LiteralPath $outPath -Force
  if ($KeepData) {
    $items = $items | Where-Object { $_.Name -cne 'data' }
  }
  foreach ($item in $items) {
    Remove-Item -LiteralPath $item.FullName -Recurse -Force
  }
}

function Resolve-PersonaSource {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$PersonaSource)

  $sourcePath = [IO.Path]::GetFullPath($PersonaSource)
  $required = @(
    'persona.md'
    'roster.md'
  )
  foreach ($name in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourcePath $name) -PathType Leaf)) {
      throw "nguồn persona thiếu file '$name' trong '$sourcePath'"
    }
  }

  return $sourcePath
}

function New-AppStage {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Repo,
    [Parameter(Mandatory)][string]$Overlay,
    [Parameter(Mandatory)][string]$StageRoot
  )

  $paths = Resolve-BuildPaths -Repo $Repo -Out $StageRoot
  $repoPath = $paths.Repo
  $stagePath = $paths.Out
  $overlayPath = [IO.Path]::GetFullPath($Overlay)

  if (-not (Test-Path -LiteralPath $overlayPath -PathType Container)) {
    throw "không thấy Portal overlay ở '$overlayPath'"
  }
  if (Test-Path -LiteralPath $stagePath) {
    throw "thư mục stage đã tồn tại: '$stagePath'"
  }

  $repoPrefix = $repoPath.TrimEnd(
    [IO.Path]::DirectorySeparatorChar,
    [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
  if ($stagePath.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "stage không được nằm trong repo nguồn '$repoPath'"
  }

  [IO.Directory]::CreateDirectory($stagePath) | Out-Null

  $trackedOutput = & git -C $repoPath --no-optional-locks ls-files -z
  if ($LASTEXITCODE -ne 0) {
    throw "git ls-files thất bại cho '$repoPath'"
  }
  $tracked = (($trackedOutput -join "`n") -split [char]0) |
    Where-Object { $_.Length -gt 0 }

  foreach ($entry in $tracked) {
    $relative = $entry.Replace('/', [IO.Path]::DirectorySeparatorChar)
    $source = Join-Path $repoPath $relative
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
      throw "Git theo dõi một file không đọc được: '$entry'"
    }
    $destination = Join-Path $stagePath $relative
    [IO.Directory]::CreateDirectory((Split-Path -Parent $destination)) | Out-Null
    Copy-Item -LiteralPath $source -Destination $destination -Force
  }

  foreach ($item in Get-ChildItem -LiteralPath $overlayPath -File -Recurse -Force) {
    $relative = [IO.Path]::GetRelativePath($overlayPath, $item.FullName)
    $destination = Join-Path $stagePath $relative
    [IO.Directory]::CreateDirectory((Split-Path -Parent $destination)) | Out-Null
    Copy-Item -LiteralPath $item.FullName -Destination $destination -Force
  }

  return $stagePath
}

function Replace-ExactlyOnce {
  param(
    [Parameter(Mandatory)][string]$Text,
    [Parameter(Mandatory)][string]$Needle,
    [Parameter(Mandatory)][string]$Replacement,
    [Parameter(Mandatory)][string]$Label
  )

  $count = 0
  $offset = 0
  while ($offset -le $Text.Length - $Needle.Length) {
    $match = $Text.IndexOf($Needle, $offset, [StringComparison]::Ordinal)
    if ($match -lt 0) { break }
    $count++
    $offset = $match + $Needle.Length
  }
  if ($count -ne 1) {
    throw "$Label`: expected exactly 1 match, found $count"
  }

  return $Text.Replace($Needle, $Replacement)
}

function Assert-SignatureAbsent {
  param(
    [Parameter(Mandatory)][string]$Text,
    [Parameter(Mandatory)][string]$Signature,
    [Parameter(Mandatory)][string]$Label
  )

  if ($Text.IndexOf($Signature, [StringComparison]::Ordinal) -ge 0) {
    throw "$Label`: inserted signature already present"
  }
}

function Apply-AppSeams {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Stage)

  $stagePath = [IO.Path]::GetFullPath($Stage)
  $serverPath = Join-Path $stagePath 'internal\daemon\server.go'
  $storePath = Join-Path $stagePath 'internal\store\store.go'
  $zaloPath = Join-Path $stagePath 'internal\daemon\zalo.go'
  $dutyPath = Join-Path $stagePath 'internal\daemon\duty.go'
  $zaloUIPath = Join-Path $stagePath 'internal\webui\static\zalo.js'

  $server = [IO.File]::ReadAllText($serverPath)
  $store = [IO.File]::ReadAllText($storePath)
  $zalo = [IO.File]::ReadAllText($zaloPath)
  $duty = [IO.File]::ReadAllText($dutyPath)
  $zaloUI = [IO.File]::ReadAllText($zaloUIPath)
  $serverNewline = if ($server.Contains("`r`n")) { "`r`n" } else { "`n" }
  $storeNewline = if ($store.Contains("`r`n")) { "`r`n" } else { "`n" }
  $zaloNewline = if ($zalo.Contains("`r`n")) { "`r`n" } else { "`n" }
  $dutyNewline = if ($duty.Contains("`r`n")) { "`r`n" } else { "`n" }
  $zaloUINewline = if ($zaloUI.Contains("`r`n")) { "`r`n" } else { "`n" }

  Assert-SignatureAbsent -Text $server -Signature 'a.registerAppRoutes(mux)' -Label 'route seam'
  Assert-SignatureAbsent -Text $store -Signature 'migrateApp(db)' -Label 'migration seam'
  Assert-SignatureAbsent -Text $zalo -Signature 'evaluateAppWorkflow(req, msg)' -Label 'workflow seam'
  Assert-SignatureAbsent -Text $duty `
    -Signature 'a.appAnswerZalo(deps, threadID, question, reply, files)' `
    -Label 'session answer seam'
  Assert-SignatureAbsent -Text $duty `
    -Signature 'a.appRunZalo(ctx, run, pz, threadID, question, appZaloCurrentMsgID(reply.ReplyQuote), history, found, files, step)' `
    -Label 'session runner seam'
  Assert-SignatureAbsent -Text $zaloUI `
    -Signature "const requestedThreadID = new URLSearchParams(window.location.search).get('thread')" `
    -Label 'Zalo deep-link declaration seam'

  $routeNeedle = "`tmux.Handle(`"POST /shutdown`", a.auth(a.handleShutdown))"
  $serverUpdated = Replace-ExactlyOnce -Text $server -Needle $routeNeedle `
    -Replacement ("`ta.registerAppRoutes(mux)" + $serverNewline + $routeNeedle) `
    -Label 'route seam'

  $migrationNeedle = "`treturn nil" + $storeNewline + '}' + $storeNewline + $storeNewline +
    '// hasColumn asks SQLite rather than tracking a version number'
  $storeUpdated = Replace-ExactlyOnce -Text $store -Needle $migrationNeedle `
    -Replacement ("`tif err := migrateApp(db); err != nil { return err }" + $storeNewline + $migrationNeedle) `
    -Label 'migration seam'

  $workflowNeedle = "`t`ta.batch.add(req.ThreadID, kind, msg.Body, delay, ipc.ZaloOutboxDraft{"
  $workflowPrefix = @(
    "`t`tdecision, workflowErr := a.evaluateAppWorkflow(req, msg)"
    "`t`tif workflowErr != nil {"
    "`t`t`ta.logger.Error(`"portal workflow failed`", `"error`", workflowErr)"
    "`t`t`tbreak"
    "`t`t}"
    "`t`tif decision != appWorkflowContinue {"
    "`t`t`tbreak"
    "`t`t}"
  ) -join $zaloNewline
  $zaloUpdated = Replace-ExactlyOnce -Text $zalo -Needle $workflowNeedle `
    -Replacement ($workflowPrefix + $zaloNewline + $workflowNeedle) `
    -Label 'workflow seam'

  $answerNeedle = @(
    "`t`tctx, cancel := context.WithTimeout(context.Background(), deps.cfg.Timeout)"
    "`t`tdefer cancel()"
    "`t`t// Bước của lượt đi vào log dùng chung, không phải một danh sách riêng của lượt:"
    "`t`t// terminal là một dòng thời gian, và một lượt đã xong vẫn nằm đó để đọc."
    "`t`tstep := func(text string) { a.zlog.add(ipc.ZaloLogStep, threadID, text) }"
    "`t`terr := a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)"
  ) -join $dutyNewline
  $answerReplacement = "`t`terr := a.appAnswerZalo(deps, threadID, question, reply, files)"
  $dutyUpdated = Replace-ExactlyOnce -Text $duty -Needle $answerNeedle `
    -Replacement $answerReplacement -Label 'session answer seam'

  $runnerNeedle = "`traw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)"
  $runnerReplacement = "`traw, err := a.appRunZalo(ctx, run, pz, threadID, question, appZaloCurrentMsgID(reply.ReplyQuote), history, found, files, step)"
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $runnerNeedle `
    -Replacement $runnerReplacement -Label 'session runner seam'

  $zaloUIDeclarationNeedle = 'let curThread = null;'
  $zaloUIDeclaration = @(
    $zaloUIDeclarationNeedle
    "const requestedThreadID = new URLSearchParams(window.location.search).get('thread');"
    'let requestedThreadHandled = false;'
  ) -join $zaloUINewline
  $zaloUIUpdated = Replace-ExactlyOnce -Text $zaloUI -Needle $zaloUIDeclarationNeedle `
    -Replacement $zaloUIDeclaration -Label 'Zalo deep-link declaration seam'

  $zaloUISelectionNeedle = '    threads = ths || [];'
  $zaloUISelection = @(
    $zaloUISelectionNeedle
    '    if (!requestedThreadHandled) {'
    '      requestedThreadHandled = true;'
    '      if (requestedThreadID && threads.some((thread) => thread.id === requestedThreadID)) {'
    '        curThread = requestedThreadID;'
    '        void refreshMemCount();'
    "        feedSig = outboxSig = listSig = '';"
    "        el('feed').textContent = '';"
    '        atBottom = true;'
    '      }'
    '    }'
  ) -join $zaloUINewline
  $zaloUIUpdated = Replace-ExactlyOnce -Text $zaloUIUpdated -Needle $zaloUISelectionNeedle `
    -Replacement $zaloUISelection -Label 'Zalo deep-link selection seam'

  $utf8NoBom = [Text.UTF8Encoding]::new($false)
  [IO.File]::WriteAllText($serverPath, $serverUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($storePath, $storeUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($zaloPath, $zaloUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($dutyPath, $dutyUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($zaloUIPath, $zaloUIUpdated, $utf8NoBom)
}

function Assert-AppPackageSensitiveContentAbsent {
  param([Parameter(Mandatory)][string]$Out)

  # Test canaries stand in for prompt, response, stderr, and credential data.
  # Scan bytes rather than decoded text so binaries and the encodings below are
  # covered with a fixed 64 KiB working buffer.
  $prefix = 'APP_TEST_'
  $needles = [Collections.Generic.List[byte[]]]::new()
  foreach ($encoding in @(
      [Text.UTF8Encoding]::new($false),
      [Text.UnicodeEncoding]::new($false, $false),
      [Text.UnicodeEncoding]::new($true, $false)
    )) {
    $needles.Add($encoding.GetBytes($prefix))
  }
  $needleArray = $needles.ToArray()

  $outPath = [IO.Path]::GetFullPath($Out)
  foreach ($file in Get-ChildItem -LiteralPath $outPath -Recurse -File -Force) {
    $relative = [IO.Path]::GetRelativePath($outPath, $file.FullName)
    try {
      $found = [AgentDCAppPackageByteScanner]::ContainsAny($file.FullName, $needleArray)
    } catch {
      throw "package content scan failed for '$relative'"
    }
    if ($found) {
      throw "package contains sensitive content in '$relative'"
    }
  }
}

function Assert-AppPackageBinaryContains {
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][string]$Signature,
    [Parameter(Mandatory)][string]$Label
  )

  $needles = [byte[][]]::new(1)
  $needles[0] = [Text.UTF8Encoding]::new($false).GetBytes($Signature)
  try {
    $found = [AgentDCAppPackageByteScanner]::ContainsAny($Path, $needles)
  } catch {
    throw "package binary scan failed for $Label"
  }
  if (-not $found) {
    throw "package binary missing $Label"
  }
}

function Assert-AppPackage {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Out,
    [switch]$AllowZaloCredentials
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  $required = @(
    'app\agentdc.exe'
    'app\transport\dist\index.js'
    'app\node\node.exe'
    'Start.vbs'
    'Stop.bat'
    'README.txt'
    'brain\wiki\index.md'
  )
  foreach ($relative in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $outPath $relative) -PathType Leaf)) {
      throw "package missing $relative"
    }
  }

  $credentials = Join-Path $outPath 'data\zalo\credentials.json'
  if (-not $AllowZaloCredentials -and (Test-Path -LiteralPath $credentials -PathType Leaf)) {
    throw 'package contains Zalo credentials'
  }

  $binary = Join-Path $outPath 'app\agentdc.exe'
  Assert-AppPackageBinaryContains -Path $binary -Signature '/memory/threads/' -Label 'Memory API signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature 'app_memory_revisions' -Label 'Memory schema signature'

  Assert-AppPackageSensitiveContentAbsent -Out $outPath

  return $outPath
}

Export-ModuleMember -Function Resolve-BuildPaths, Assert-CleanGitSource, Get-AppGoTestSkipPattern, Clear-AppOutput, Resolve-PersonaSource, New-AppStage, Apply-AppSeams, Assert-AppPackage
