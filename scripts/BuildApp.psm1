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

if (-not ('AgentDCAppPhysicalPath' -as [type])) {
  Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

public static class AgentDCAppPhysicalPath
{
    private const uint OpenExisting = 3;
    private const uint BackupSemantics = 0x02000000;
    private const uint VolumeNameGuid = 0x1;

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern SafeFileHandle CreateFileW(
        string fileName,
        uint desiredAccess,
        FileShare shareMode,
        IntPtr securityAttributes,
        uint creationDisposition,
        uint flagsAndAttributes,
        IntPtr templateFile);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern uint GetFinalPathNameByHandleW(
        SafeFileHandle file,
        StringBuilder path,
        uint pathLength,
        uint flags);

    public static string GetFinalVolumePath(string path)
    {
        using (SafeFileHandle handle = CreateFileW(
            path, 0, FileShare.Read | FileShare.Write | FileShare.Delete, IntPtr.Zero,
            OpenExisting, BackupSemantics, IntPtr.Zero))
        {
            if (handle.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
            StringBuilder buffer = new StringBuilder(32768);
            uint length = GetFinalPathNameByHandleW(handle, buffer, (uint)buffer.Capacity, VolumeNameGuid);
            if (length == 0) throw new Win32Exception(Marshal.GetLastWin32Error());
            if (length >= buffer.Capacity) throw new IOException("physical path exceeds verifier buffer");
            return buffer.ToString();
        }
    }
}
'@
}

function Get-AppPhysicalPathIdentity {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Path)

  try {
    $fullPath = [IO.Path]::GetFullPath($Path)
    $candidate = $fullPath
    $suffix = [Collections.Generic.List[string]]::new()
    while (-not (Test-Path -LiteralPath $candidate -ErrorAction Stop)) {
      $trimmed = $candidate.TrimEnd(
        [IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
      $leaf = [IO.Path]::GetFileName($trimmed)
      $parent = [IO.Path]::GetDirectoryName($trimmed)
      if ([string]::IsNullOrWhiteSpace($leaf) -or [string]::IsNullOrWhiteSpace($parent) -or
          $parent.Equals($candidate, [StringComparison]::OrdinalIgnoreCase)) {
        throw "no existing ancestor for '$fullPath'"
      }
      $suffix.Insert(0, $leaf)
      $candidate = $parent
    }

    $identity = [AgentDCAppPhysicalPath]::GetFinalVolumePath($candidate).Replace('/', '\').TrimEnd('\')
    if ([string]::IsNullOrWhiteSpace($identity)) {
      throw "empty physical identity for '$candidate'"
    }
    foreach ($part in $suffix) {
      $identity += '\' + $part
    }
    return $identity.TrimEnd('\')
  } catch {
    throw "physical path canonicalization failed for '$Path': $($_.Exception.Message)"
  }
}

function Test-AppPhysicalPathsOverlap {
  param(
    [Parameter(Mandatory)][string]$Left,
    [Parameter(Mandatory)][string]$Right
  )

  $leftKey = $Left.TrimEnd('\', '/')
  $rightKey = $Right.TrimEnd('\', '/')
  $leftPrefix = $leftKey + '\'
  $rightPrefix = $rightKey + '\'
  return $leftKey.Equals($rightKey, [StringComparison]::OrdinalIgnoreCase) -or
    $leftKey.StartsWith($rightPrefix, [StringComparison]::OrdinalIgnoreCase) -or
    $rightKey.StartsWith($leftPrefix, [StringComparison]::OrdinalIgnoreCase)
}

function Assert-AppPathHasNoReparsePoint {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Path,
    [switch]$InspectExistingTree
  )

  $fullPath = [IO.Path]::GetFullPath($Path)
  $root = [IO.Path]::GetPathRoot($fullPath)
  $current = $root
  $relative = $fullPath.Substring($root.Length)
  $separators = [char[]]@(
    [IO.Path]::DirectorySeparatorChar,
    [IO.Path]::AltDirectorySeparatorChar
  )
  foreach ($part in $relative.Split($separators, [StringSplitOptions]::RemoveEmptyEntries)) {
    $current = Join-Path $current $part
    if (-not (Test-Path -LiteralPath $current -ErrorAction Stop)) { break }
    $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "output path contains a reparse point: '$($item.FullName)'"
    }
  }

  if (-not $InspectExistingTree -or
      -not (Test-Path -LiteralPath $fullPath -PathType Container -ErrorAction Stop)) {
    return $fullPath
  }

  $pending = [Collections.Generic.Stack[string]]::new()
  $pending.Push($fullPath)
  while ($pending.Count -gt 0) {
    $directory = $pending.Pop()
    foreach ($child in @(Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop)) {
      if (($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "output tree contains a reparse point: '$($child.FullName)'"
      }
      if ($child.PSIsContainer) {
        $pending.Push($child.FullName)
      }
    }
  }
  return $fullPath
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

  $repoIdentity = Get-AppPhysicalPathIdentity -Path $repoPath
  $outIdentity = Get-AppPhysicalPathIdentity -Path $outPath
  if (Test-AppPhysicalPathsOverlap -Left $repoIdentity -Right $outIdentity) {
    throw "repo và output có physical path trùng hoặc nằm bên trong nhau: '$repoPath', '$outPath'"
  }
  Assert-AppPathHasNoReparsePoint -Path $outPath -InspectExistingTree | Out-Null

  [PSCustomObject]@{
    Repo = $repoPath
    Out = $outPath
    RepoIdentity = $repoIdentity
    OutIdentity = $outIdentity
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
    # The integrated no-default route intentionally keeps a fresh bot silent until a Provider
    # and active Combo are configured. The upstream-only join greeting remains covered in the
    # pristine source repository; the stitched package must skip this one exact incompatible test.
    'TestJoinGreetsOnceForEveryone'
  )
  $escaped = $names | ForEach-Object { [regex]::Escape($_) }
  return '^(?:' + ($escaped -join '|') + ')$'
}

function Clear-AppOutput {
  [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Low')]
  param(
    [Parameter(Mandatory)][string]$Out,
    [Parameter(Mandatory)][string[]]$ProtectedRoot,
    [string]$ExpectedOutIdentity,
    [switch]$KeepData
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  $volumeRoot = [IO.Path]::GetPathRoot($outPath)
  if ($outPath.TrimEnd('\', '/').Equals($volumeRoot.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase)) {
    throw "từ chối xóa nội dung thư mục gốc '$outPath'"
  }
  if (-not (Test-Path -LiteralPath $outPath)) { return }

  # Re-resolve physical identities immediately before authorizing any deletion. Lexical equality
  # is insufficient on Windows because extended, 8.3, SUBST and volume aliases may name the same
  # directory. Canonicalization failure is fatal; deletion never falls back to a lexical check.
  $currentOutIdentity = Get-AppPhysicalPathIdentity -Path $outPath
  if (-not [string]::IsNullOrWhiteSpace($ExpectedOutIdentity) -and
      -not $currentOutIdentity.Equals(
        $ExpectedOutIdentity.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase)) {
    throw "output physical identity changed since validation: '$outPath'"
  }
  foreach ($protectedPath in $ProtectedRoot) {
    $protectedIdentity = Get-AppPhysicalPathIdentity -Path $protectedPath
    if (Test-AppPhysicalPathsOverlap -Left $currentOutIdentity -Right $protectedIdentity) {
      throw "output physical path overlaps protected root: '$outPath', '$protectedPath'"
    }
  }

  # Validate every existing component and descendant before the first removal. In particular,
  # never recurse through a junction/symlink whose target may be outside the requested output.
  Assert-AppPathHasNoReparsePoint -Path $outPath -InspectExistingTree | Out-Null

  $items = Get-ChildItem -LiteralPath $outPath -Force -ErrorAction Stop
  if ($KeepData) {
    $items = $items | Where-Object { $_.Name -cne 'data' }
  }
  foreach ($item in $items) {
    if ($PSCmdlet.ShouldProcess($item.FullName, 'Remove build output item')) {
      Remove-Item -LiteralPath $item.FullName -Recurse -Force -ErrorAction Stop
    }
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

function Assert-CleanGitOverlay {
  param(
    [Parameter(Mandatory)][string]$Overlay,
    [switch]$RequireGit
  )

  $overlayPath = [IO.Path]::GetFullPath($Overlay)
  $gitRootOutput = & git -C $overlayPath --no-optional-locks rev-parse --show-toplevel 2>$null
  if ($LASTEXITCODE -ne 0) {
    if ($RequireGit) {
      throw "Portal overlay không nằm trong Git worktree: '$overlayPath'"
    }
    # Unit fixtures may be plain directories. Production overlays live in this
    # worktree and are checked below before any of their files are staged.
    return
  }

  $gitRoot = [IO.Path]::GetFullPath(($gitRootOutput -join '').Trim())
  $relative = [IO.Path]::GetRelativePath($gitRoot, $overlayPath).Replace('\', '/')
  $status = (& git -C $gitRoot --no-optional-locks status --porcelain=v1 `
      --untracked-files=all -- $relative) -join "`n"
  if ($LASTEXITCODE -ne 0) {
    throw "không đọc được Git status của Portal overlay '$overlayPath'"
  }
  if (-not [string]::IsNullOrWhiteSpace($status)) {
    throw "Portal overlay không sạch; commit hoặc cất thay đổi trước khi build: '$overlayPath'`n$status"
  }
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
  $productionOverlay = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\appmode\overlay'))
  $requiresGit = $overlayPath.Equals($productionOverlay, [StringComparison]::OrdinalIgnoreCase)
  Assert-CleanGitOverlay -Overlay $overlayPath -RequireGit:$requiresGit
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

function Assert-AppProviderRouteTopology {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][ValidateSet(0, 1)][int]$ExpectedAnswerCallCount
  )

  $sourcePath = [IO.Path]::GetFullPath($Path)
  if (-not (Test-Path -LiteralPath $sourcePath -PathType Container)) {
    throw "provider route topology verification failed: daemon source directory is missing '$sourcePath'"
  }

  $goCommand = Get-Command go -CommandType Application -ErrorAction Stop
  $tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
  $verifierRoot = Join-Path $tempBase ('portal-go-ast-' + [guid]::NewGuid().ToString('N'))
  $verifierPath = Join-Path $verifierRoot 'verify.go'
  $verifierSource = @'
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

func selector(expr ast.Expr, receiver, method string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == receiver
}

func namedFunction(files map[string]*ast.File, name string) (*ast.FuncDecl, int) {
	var found *ast.FuncDecl
	count := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == name {
				found = fn
				count++
			}
		}
	}
	return found, count
}

func routeAssignment(node ast.Node) (token.Pos, bool) {
	assignment, ok := node.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
		return token.NoPos, false
	}
	left, leftOK := assignment.Lhs[0].(*ast.Ident)
	call, callOK := assignment.Rhs[0].(*ast.CallExpr)
	if !leftOK || left.Name != "run" || !callOK {
		return token.NoPos, false
	}
	fun, funOK := call.Fun.(*ast.Ident)
	if !funOK || fun.Name != "route" {
		return token.NoPos, false
	}
	return call.Pos(), true
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "--" {
		fail("expected one Go daemon directory and answer-call count")
	}
	expectedAnswerCalls, err := strconv.Atoi(os.Args[3])
	if err != nil || (expectedAnswerCalls != 0 && expectedAnswerCalls != 1) {
		fail("expected answer-call count must be zero or one")
	}
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, os.Args[2], func(info os.FileInfo) bool {
		return !info.IsDir() && strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		fail("parse production daemon package: %v", err)
	}
	pkg, ok := packages["daemon"]
	if !ok || len(packages) != 1 {
		fail("expected exactly one production daemon package, found %d", len(packages))
	}
	files := pkg.Files

	answer, answerCount := namedFunction(files, "appAnswerZalo")
	factory, factoryCount := namedFunction(files, "appAnswerZaloWithRunnerFactory")
	if answerCount != 1 || answer == nil || answer.Body == nil {
		fail("expected exactly one appAnswerZalo function, found %d", answerCount)
	}
	if factoryCount != 1 || factory == nil || factory.Body == nil {
		fail("expected exactly one appAnswerZaloWithRunnerFactory function, found %d", factoryCount)
	}

	totalRunnerSelectors := 0
	answerEntrypointSelectors := 0
	answerEntrypointCalls := 0
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			if expr, ok := node.(ast.Expr); ok && selector(expr, "a", "appZaloRunner") {
				totalRunnerSelectors++
			}
			if expr, ok := node.(ast.Expr); ok && selector(expr, "a", "appAnswerZalo") {
				answerEntrypointSelectors++
			}
			if call, ok := node.(*ast.CallExpr); ok && selector(call.Fun, "a", "appAnswerZalo") {
				answerEntrypointCalls++
			}
			return true
		})
	}
	if answerEntrypointSelectors != expectedAnswerCalls || answerEntrypointCalls != expectedAnswerCalls {
		fail("expected %d direct appAnswerZalo calls and no other references in the production daemon (references=%d, direct calls=%d)",
			expectedAnswerCalls, answerEntrypointSelectors, answerEntrypointCalls)
	}

	participatingRunnerArgs := 0
	answerFactoryCalls := 0
	ast.Inspect(answer.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !selector(call.Fun, "a", "appAnswerZaloWithRunnerFactory") {
			return true
		}
		answerFactoryCalls++
		for _, arg := range call.Args {
			if selector(arg, "a", "appZaloRunner") {
				participatingRunnerArgs++
			}
		}
		return true
	})
	if answerFactoryCalls != 1 || participatingRunnerArgs != 1 || totalRunnerSelectors != 1 {
		fail("expected one appZaloRunner argument in appAnswerZalo (calls=%d, arguments=%d, total selectors=%d)",
			answerFactoryCalls, participatingRunnerArgs, totalRunnerSelectors)
	}

	globalRouteAssignments := 0
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			if _, ok := routeAssignment(node); ok {
				globalRouteAssignments++
			}
			return true
		})
	}

	acquireCalls := 0
	var acquirePosition token.Pos
	factoryRouteAssignments := 0
	var routePosition token.Pos
	ast.Inspect(factory.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && selector(call.Fun, "appZaloProcessThreadGate", "Acquire") {
			acquireCalls++
			acquirePosition = call.Pos()
		}
		if position, ok := routeAssignment(node); ok {
			factoryRouteAssignments++
			routePosition = position
		}
		return true
	})
	if globalRouteAssignments != 1 || acquireCalls != 1 || factoryRouteAssignments != 1 || routePosition <= acquirePosition {
		fail("expected the sole global route assignment inside the factory after one thread-gate acquire (global routes=%d, acquires=%d, factory routes=%d)",
			globalRouteAssignments, acquireCalls, factoryRouteAssignments)
	}
}
'@

  [IO.Directory]::CreateDirectory($verifierRoot) | Out-Null
  try {
    Assert-AppPathHasNoReparsePoint -Path $verifierRoot -InspectExistingTree | Out-Null
    [IO.File]::WriteAllText($verifierPath, $verifierSource, [Text.UTF8Encoding]::new($false))

    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $goCommand.Source
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.ArgumentList.Add('run')
    $start.ArgumentList.Add($verifierPath)
    $start.ArgumentList.Add('--')
    $start.ArgumentList.Add($sourcePath)
    $start.ArgumentList.Add($ExpectedAnswerCallCount.ToString([Globalization.CultureInfo]::InvariantCulture))
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
      if (-not $process.Start()) {
        throw 'Go AST verifier process did not start'
      }
      if (-not $process.WaitForExit(30000)) {
        try { $process.Kill($true) } catch { $process.Kill() }
        throw 'Go AST verifier exceeded 30000ms'
      }
      $stdout = $process.StandardOutput.ReadToEnd().Trim()
      $stderr = $process.StandardError.ReadToEnd().Trim()
      if ($process.ExitCode -ne 0) {
        $detail = (@($stderr, $stdout) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) -join '; '
        throw "Go AST verifier rejected the source (exit $($process.ExitCode)): $detail"
      }
    } finally {
      $process.Dispose()
    }
  } catch {
    throw "provider route topology verification failed: $($_.Exception.Message)"
  } finally {
    if (Test-Path -LiteralPath $verifierRoot) {
      $tempPrefix = $tempBase.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) +
        [IO.Path]::DirectorySeparatorChar
      if (-not $verifierRoot.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "refusing to clean Go AST verifier outside the temporary directory: '$verifierRoot'"
      }
      Assert-AppPathHasNoReparsePoint -Path $verifierRoot -InspectExistingTree | Out-Null
      [IO.Directory]::Delete($verifierRoot, $true)
    }
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
  $lessonPath = Join-Path $stagePath 'internal\daemon\zalolesson.go'
  $zaloUIPath = Join-Path $stagePath 'internal\webui\static\zalo.js'
  $daemonPath = Join-Path $stagePath 'internal\daemon'

  $server = [IO.File]::ReadAllText($serverPath)
  $store = [IO.File]::ReadAllText($storePath)
  $zalo = [IO.File]::ReadAllText($zaloPath)
  $duty = [IO.File]::ReadAllText($dutyPath)
  $lesson = [IO.File]::ReadAllText($lessonPath)
  $zaloUI = [IO.File]::ReadAllText($zaloUIPath)
  $serverNewline = if ($server.Contains("`r`n")) { "`r`n" } else { "`n" }
  $storeNewline = if ($store.Contains("`r`n")) { "`r`n" } else { "`n" }
  $zaloNewline = if ($zalo.Contains("`r`n")) { "`r`n" } else { "`n" }
  $dutyNewline = if ($duty.Contains("`r`n")) { "`r`n" } else { "`n" }
  $lessonNewline = if ($lesson.Contains("`r`n")) { "`r`n" } else { "`n" }
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
  Assert-SignatureAbsent -Text $duty -Signature 'ProgramPrefixArgs []string' `
    -Label 'managed Claude program seam'
  Assert-SignatureAbsent -Text $duty -Signature 'var ErrZaloSilent = errors.New(' `
    -Label 'no-provider sentinel seam'
  Assert-SignatureAbsent -Text $duty -Signature 'errors.Is(err, ErrZaloSilent)' `
    -Label 'no-provider answer seam'
  Assert-SignatureAbsent -Text $duty -Signature 'CLAUDE_CONFIG_DIR=' `
    -Label 'Claude account isolation seam'
  Assert-SignatureAbsent -Text $lesson `
    -Signature 'a.st.CreateAppLesson(lesson)' `
    -Label 'operator lesson seam'
  Assert-SignatureAbsent -Text $zaloUI `
    -Signature "const requestedThreadID = new URLSearchParams(window.location.search).get('thread')" `
    -Label 'Zalo deep-link declaration seam'
  Assert-AppProviderRouteTopology -Path $daemonPath -ExpectedAnswerCallCount 0

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

  $configNeedle = "`tModel string"
  $configReplacement = @(
    "`tModel string"
    "`t// ConfigDir isolates each connected Claude account from the machine default."
    "`tConfigDir string"
    "`t// Program and ProgramPrefixArgs opt into the managed npm CLI; empty keeps upstream lookup."
    "`tProgram string"
    "`tProgramPrefixArgs []string"
  ) -join $dutyNewline
  $dutyUpdated = Replace-ExactlyOnce -Text $duty -Needle $configNeedle `
    -Replacement $configReplacement -Label 'managed Claude config seam'

  $answerErrorNeedle = @(
    "`tif err != nil {"
    "`t`treturn escalate(`"the agent did not finish`", `"err`", err)"
    "`t}"
  ) -join $dutyNewline
  $answerErrorReplacement = @(
    "`tif errors.Is(err, ErrZaloSilent) {"
    "`t`treturn nil"
    "`t}"
    "`tif err != nil {"
    "`t`treturn escalate(`"the agent did not finish`", `"err`", err)"
    "`t}"
  ) -join $dutyNewline
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $answerErrorNeedle `
    -Replacement $answerErrorReplacement -Label 'no-provider answer seam'

  $silentNeedle = 'const maxZaloAnswers = 6'
  $silentReplacement = @(
    '// ErrZaloSilent means a deliberately unconfigured Provider route; answerZalo must stay quiet.'
    'var ErrZaloSilent = errors.New("zalo: im lặng — chưa cấu hình provider")'
    ''
    $silentNeedle
  ) -join $dutyNewline
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $silentNeedle `
    -Replacement $silentReplacement -Label 'no-provider sentinel seam'

  $programNeedle = @(
    "`tprof := agent.ConsultReadOnly()"
    "`tbin, err := exec.LookPath(prof.Binary)"
    "`tif err != nil {"
    "`t`treturn `"`", fmt.Errorf(`"locate %s: %w`", prof.Binary, err)"
    "`t}"
    "`targs, err := consultArgv(e.cfg, uuid.NewString())"
    "`tif err != nil {"
    "`t`treturn `"`", err"
    "`t}"
    "`tcmd := exec.CommandContext(ctx, bin, args...)"
  ) -join $dutyNewline
  $programReplacement = @(
    "`tprof := agent.ConsultReadOnly()"
    "`tbin := e.cfg.Program"
    "`tprefixArgs := slices.Clone(e.cfg.ProgramPrefixArgs)"
    "`tif bin == `"`" {"
    "`t`tvar err error"
    "`t`tbin, err = exec.LookPath(prof.Binary)"
    "`t`tif err != nil {"
    "`t`t`treturn `"`", fmt.Errorf(`"locate %s: %w`", prof.Binary, err)"
    "`t`t}"
    "`t}"
    "`targs, err := consultArgv(e.cfg, uuid.NewString())"
    "`tif err != nil {"
    "`t`treturn `"`", err"
    "`t}"
    "`targs = append(prefixArgs, args...)"
    "`tcmd := exec.CommandContext(ctx, bin, args...)"
  ) -join $dutyNewline
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $programNeedle `
    -Replacement $programReplacement -Label 'managed Claude program seam'

  $envNeedle = "`tcmd.Env = prof.Env(os.Environ())" + $dutyNewline +
    "`tif e.cfg.ThinkingTokens > 0 {"
  $envReplacement = @(
    "`tcmd.Env = prof.Env(os.Environ())"
    "`tif e.cfg.ConfigDir != `"`" {"
    "`t`tcmd.Env = append(cmd.Env, `"CLAUDE_CONFIG_DIR=`"+e.cfg.ConfigDir)"
    "`t}"
    "`tif e.cfg.ThinkingTokens > 0 {"
  ) -join $dutyNewline
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $envNeedle `
    -Replacement $envReplacement -Label 'Claude account isolation seam'

  $answerNeedle = @(
    "`t`tctx, cancel := context.WithTimeout(context.Background(), deps.cfg.Timeout)"
    "`t`tdefer cancel()"
    "`t`t// Bước của lượt đi vào log dùng chung, không phải một danh sách riêng của lượt:"
    "`t`t// terminal là một dòng thời gian, và một lượt đã xong vẫn nằm đó để đọc."
    "`t`tstep := func(text string) { a.zlog.add(ipc.ZaloLogStep, threadID, text) }"
    "`t`terr := a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)"
  ) -join $dutyNewline
  $answerReplacement = "`t`terr := a.appAnswerZalo(deps, threadID, question, reply, files)"
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $answerNeedle `
    -Replacement $answerReplacement -Label 'session answer seam'

  $runnerNeedle = "`traw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)"
  $runnerReplacement = "`traw, err := a.appRunZalo(ctx, run, pz, threadID, question, appZaloCurrentMsgID(reply.ReplyQuote), history, found, files, step)"
  $dutyUpdated = Replace-ExactlyOnce -Text $dutyUpdated -Needle $runnerNeedle `
    -Replacement $runnerReplacement -Label 'session runner seam'

  $lessonImportNeedle = "`t" + '"agentdc/internal/ipc"'
  $lessonImportReplacement = @(
    $lessonImportNeedle
    ("`t" + '"agentdc/internal/store"')
  ) -join $lessonNewline
  $lessonUpdated = Replace-ExactlyOnce -Text $lesson -Needle $lessonImportNeedle `
    -Replacement $lessonImportReplacement -Label 'operator lesson import seam'

  $lessonCommentNeedle = @(
    '// Ghi vào zalo_memory như mọi ghi chú khác, nên nó cũng KHÔNG trích dẫn được — xem chú thích bảng'
    '// đó. Một bài học là chữ do hệ này tự sinh, tức đúng thứ không được thành nguồn.'
  ) -join $lessonNewline
  $lessonCommentReplacement = @(
    '// Ghi thành bài học có cấu trúc để Portal duyệt được và mọi phiên CLI nhận refresh toàn cục.'
    '// Đây là quy tắc chất lượng câu trả lời, không phải một sự thật về khách hàng.'
  ) -join $lessonNewline
  $lessonUpdated = Replace-ExactlyOnce -Text $lessonUpdated -Needle $lessonCommentNeedle `
    -Replacement $lessonCommentReplacement -Label 'operator lesson comment seam'

  $legacyLessonNeedle = [regex]::Replace((@'
	text := "người trực sửa lại: bot nói \"" + clip(strings.TrimSpace(bot.Body), maxLessonPart) +
		"\" → người trực gửi \"" + clip(strings.TrimSpace(operatorText), maxLessonPart) + "\""
	if err := a.st.AddZaloMemory(ipc.ZaloMemory{ThreadID: threadID, Text: text}); err != nil {
'@).TrimEnd([char[]]"`r`n"), '\r?\n', $lessonNewline)
  $structuredLessonReplacement = [regex]::Replace((@'
	lesson := store.AppLessonInput{
		ThreadID: threadID,
		BotText:  clip(strings.TrimSpace(bot.Body), maxLessonPart),
		Better:   clip(strings.TrimSpace(operatorText), maxLessonPart),
		Note:     "người trực sửa lại câu trả lời của bot",
	}
	if _, err := a.st.CreateAppLesson(lesson); err != nil {
'@).TrimEnd([char[]]"`r`n"), '\r?\n', $lessonNewline)
  $lessonUpdated = Replace-ExactlyOnce -Text $lessonUpdated -Needle $legacyLessonNeedle `
    -Replacement $structuredLessonReplacement -Label 'operator lesson seam'

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
  [IO.File]::WriteAllText($lessonPath, $lessonUpdated, $utf8NoBom)
  [IO.File]::WriteAllText($zaloUIPath, $zaloUIUpdated, $utf8NoBom)
  Assert-AppProviderRouteTopology -Path $daemonPath -ExpectedAnswerCallCount 1
}

function Get-AppSafePackageFiles {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Root)

  $rootPath = [IO.Path]::GetFullPath($Root)
  Assert-AppPathHasNoReparsePoint -Path $rootPath | Out-Null
  if (-not (Test-Path -LiteralPath $rootPath -PathType Container -ErrorAction Stop)) {
    throw "package root is not a readable directory: '$rootPath'"
  }

  $pending = [Collections.Generic.Stack[string]]::new()
  $pending.Push($rootPath)
  while ($pending.Count -gt 0) {
    $directory = $pending.Pop()
    foreach ($child in @(Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop)) {
      if (($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "package tree contains a reparse point: '$($child.FullName)'"
      }
      if ($child.PSIsContainer) {
        $pending.Push($child.FullName)
      } else {
        Write-Output $child
      }
    }
  }
}

function Assert-AppPackageSensitiveContentAbsent {
  param(
    [Parameter(Mandatory)][string]$Out,
    [IO.FileInfo[]]$Files
  )

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
  if ($null -eq $Files) {
    $Files = @(Get-AppSafePackageFiles -Root $outPath)
  }
  foreach ($file in $Files) {
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

$script:ProviderCredentialCanary = 'sk-package-must-never-contain-7f36d2'

function Assert-AppProviderCredentialFilesAbsent {
  param(
    [Parameter(Mandatory)][string]$Package,
    [Parameter(Mandatory)][string]$Canary,
    [Parameter(Mandatory)][IO.FileInfo[]]$Files
  )

  $packagePath = [IO.Path]::GetFullPath($Package)
  $needle = [byte[][]]::new(3)
  $needle[0] = [Text.UTF8Encoding]::new($false).GetBytes($Canary)
  $needle[1] = [Text.UnicodeEncoding]::new($false, $false).GetBytes($Canary)
  $needle[2] = [Text.UnicodeEncoding]::new($true, $false).GetBytes($Canary)
  # The unique exact canary has no exemption: not arbitrary node_modules and not bundled npm.
  # A heuristic scanner may need a narrow npm exception, but this exact byte signature never does.
  foreach ($file in $Files) {
    $relative = [IO.Path]::GetRelativePath($packagePath, $file.FullName)
    try {
      $found = [AgentDCAppPackageByteScanner]::ContainsAny($file.FullName, $needle)
    } catch {
      throw "provider credential scan failed for '$relative': $($_.Exception.Message)"
    }
    if ($found) {
      throw "package contains plaintext provider credential: $relative"
    }
  }
}

function Assert-NoProviderCredential {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Root,
    [string]$Canary = $script:ProviderCredentialCanary
  )

  $rootPath = [IO.Path]::GetFullPath($Root)
  if (-not (Test-Path -LiteralPath $rootPath -PathType Container)) {
    throw "không thấy gói ở '$rootPath'"
  }
  $files = @(Get-AppSafePackageFiles -Root $rootPath)
  Assert-AppProviderCredentialFilesAbsent -Package $rootPath -Canary $Canary -Files $files

  return $rootPath
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

function Invoke-AppPackagedNpmRuntimeSmoke {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$NodePath,
    [Parameter(Mandatory)][string]$NpmCLIPath,
    [ValidateRange(1000, 60000)][int]$TimeoutMilliseconds = 15000
  )

  foreach ($path in @($NodePath, $NpmCLIPath)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf -ErrorAction Stop)) {
      throw "packaged npm runtime input is missing: '$path'"
    }
  }

  $start = [Diagnostics.ProcessStartInfo]::new()
  $start.FileName = [IO.Path]::GetFullPath($NodePath)
  $start.WorkingDirectory = Split-Path -Parent ([IO.Path]::GetFullPath($NodePath))
  $start.UseShellExecute = $false
  $start.CreateNoWindow = $true
  $start.RedirectStandardOutput = $true
  $start.RedirectStandardError = $true
  $start.ArgumentList.Add([IO.Path]::GetFullPath($NpmCLIPath))
  $start.ArgumentList.Add('--version')
  $process = [Diagnostics.Process]::new()
  $process.StartInfo = $start
  try {
    try {
      if (-not $process.Start()) {
        throw 'process did not start'
      }
    } catch {
      throw "packaged npm runtime could not start: $($_.Exception.Message)"
    }
    if (-not $process.WaitForExit($TimeoutMilliseconds)) {
      try { $process.Kill($true) } catch { $process.Kill() }
      throw "packaged npm runtime exceeded ${TimeoutMilliseconds}ms"
    }
    $stdout = $process.StandardOutput.ReadToEnd().Trim()
    $null = $process.StandardError.ReadToEnd()
    if ($process.ExitCode -ne 0 -or $stdout -notmatch '^\d+\.\d+\.\d+(?:\s|$)') {
      throw "packaged npm runtime failed its local version smoke (exit $($process.ExitCode))"
    }
    return $stdout
  } finally {
    $process.Dispose()
  }
}

function Invoke-AppPackagedNpmCmdSmoke {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$NpmCmdPath,
    [Parameter(Mandatory)][string]$ExpectedVersion,
    [ValidateRange(1000, 60000)][int]$TimeoutMilliseconds = 15000
  )

  $shimPath = [IO.Path]::GetFullPath($NpmCmdPath)
  if (-not (Test-Path -LiteralPath $shimPath -PathType Leaf -ErrorAction Stop)) {
    throw "packaged npm.cmd is missing: '$shimPath'"
  }
  $shim = [IO.File]::ReadAllText($shimPath)
  foreach ($marker in @(
      '%~dp0\node.exe',
      '%~dp0\node_modules\npm\bin\npm-cli.js',
      '"%NODE_EXE%" "%NPM_CLI_JS%" %*'
    )) {
    if ($shim.IndexOf($marker, [StringComparison]::OrdinalIgnoreCase) -lt 0) {
      throw "packaged npm.cmd does not delegate through its adjacent node/npm-cli runtime"
    }
  }
  if ($shimPath.Contains('"')) {
    throw 'packaged npm.cmd path contains an unsafe quote character'
  }

  $cmdPath = Join-Path $env:SystemRoot 'System32\cmd.exe'
  if (-not (Test-Path -LiteralPath $cmdPath -PathType Leaf -ErrorAction Stop)) {
    throw "packaged npm shim smoke cannot locate cmd.exe"
  }
  $start = [Diagnostics.ProcessStartInfo]::new()
  $start.FileName = $cmdPath
  $start.WorkingDirectory = Split-Path -Parent $shimPath
  $start.UseShellExecute = $false
  $start.CreateNoWindow = $true
  $start.RedirectStandardOutput = $true
  $start.RedirectStandardError = $true
  # Keep the command string fixed and carry the untrusted absolute path in the environment.
  # cmd expands the variable once inside quotes, so spaces/metacharacters cannot become syntax.
  $start.EnvironmentVariables['AGENTDC_PACKAGED_NPM_CMD'] = $shimPath
  $start.Arguments = '/d /s /c ""%AGENTDC_PACKAGED_NPM_CMD%" --version"'
  $process = [Diagnostics.Process]::new()
  $process.StartInfo = $start
  try {
    try {
      if (-not $process.Start()) {
        throw 'process did not start'
      }
    } catch {
      throw "packaged npm.cmd could not start: $($_.Exception.Message)"
    }
    if (-not $process.WaitForExit($TimeoutMilliseconds)) {
      try { $process.Kill($true) } catch { $process.Kill() }
      throw "packaged npm.cmd exceeded ${TimeoutMilliseconds}ms"
    }
    $stdout = $process.StandardOutput.ReadToEnd().Trim()
    $null = $process.StandardError.ReadToEnd()
    if ($process.ExitCode -ne 0 -or $stdout -notmatch '^\d+\.\d+\.\d+(?:\s|$)' -or
        -not $stdout.Equals($ExpectedVersion.Trim(), [StringComparison]::Ordinal)) {
      throw "packaged npm.cmd failed to delegate the local version smoke (exit $($process.ExitCode))"
    }
    return $stdout
  } finally {
    $process.Dispose()
  }
}

function Assert-AppPackage {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Out,
    [switch]$AllowZaloCredentials
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  if (-not (Test-Path -LiteralPath $outPath -PathType Container -ErrorAction Stop)) {
    throw 'package missing app\agentdc.exe'
  }
  $packageFiles = @(Get-AppSafePackageFiles -Root $outPath)
  $required = @(
    'app\agentdc.exe'
    'app\transport\dist\index.js'
    'app\node\node.exe'
    'app\node\npm.cmd'
    'app\node\node_modules\npm\bin\npm-cli.js'
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
  Assert-AppPackageBinaryContains -Path $binary -Signature 'app_memory_subject_revisions' -Label 'Memory V2 schema signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature 'memory_ops' -Label 'Memory V2 prompt contract'
  Assert-AppPackageBinaryContains -Path $binary -Signature '/memory/threads/{tid}/{id}/approve' -Label 'Memory V2 proposal API'
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature 'người trực sửa lại câu trả lời của bot' `
    -Label 'structured operator lesson'
  Assert-AppPackageBinaryContains -Path $binary -Signature '/llm/providers' -Label 'Provider API signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature '/llm/combos' -Label 'Combo API signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature 'llm_providers' -Label 'Provider schema signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature 'llm_combos' -Label 'Combo schema signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature '@openai/codex' -Label 'Codex CLI package signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature 'CODEX_HOME' -Label 'Codex account isolation signature'
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature 'app_zalo_cli_sessions' -Label 'Zalo session schema signature'
  Assert-AppPackageBinaryContains -Path $binary -Signature 'llm_accounts' -Label 'LLM account schema signature'
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature '@anthropic-ai/claude-code' -Label 'Claude CLI package signature'
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature 'CLAUDE_CONFIG_DIR' -Label 'Claude account isolation signature'
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature 'zalo: im lặng — chưa cấu hình provider' -Label 'silent no-provider route signature'

  $packagedNode = Join-Path $outPath 'app\node\node.exe'
  $packagedNpmCmd = Join-Path $outPath 'app\node\npm.cmd'
  $packagedNpmCLI = Join-Path $outPath 'app\node\node_modules\npm\bin\npm-cli.js'
  $packagedNpmVersion = Invoke-AppPackagedNpmRuntimeSmoke `
    -NodePath $packagedNode -NpmCLIPath $packagedNpmCLI
  Invoke-AppPackagedNpmCmdSmoke -NpmCmdPath $packagedNpmCmd `
    -ExpectedVersion $packagedNpmVersion | Out-Null

  Assert-AppPackageSensitiveContentAbsent -Out $outPath -Files $packageFiles
  Assert-AppProviderCredentialFilesAbsent -Package $outPath `
    -Canary $script:ProviderCredentialCanary -Files $packageFiles

  return $outPath
}

Export-ModuleMember -Function Resolve-BuildPaths, Assert-CleanGitSource, Get-AppGoTestSkipPattern, Clear-AppOutput, Resolve-PersonaSource, New-AppStage, Apply-AppSeams, Assert-AppProviderRouteTopology, Assert-NoProviderCredential, Assert-AppPackage
