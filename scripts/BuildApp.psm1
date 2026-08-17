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

if (-not ('AgentDCAppPersonaSnapshot' -as [type])) {
  Add-Type -TypeDefinition @'
using System;
using System.IO;

public sealed class AgentDCAppPersonaSnapshotFile
{
    private readonly byte[] content;

    public AgentDCAppPersonaSnapshotFile(string path, byte[] content, string sha256)
    {
        if (String.IsNullOrEmpty(path)) throw new ArgumentException("empty path", nameof(path));
        if (content == null) throw new ArgumentNullException(nameof(content));
        if (String.IsNullOrEmpty(sha256)) throw new ArgumentException("empty digest", nameof(sha256));
        Path = path;
        this.content = (byte[])content.Clone();
        SHA256 = sha256;
    }

    public string Path { get; }
    public ulong Length { get { return (ulong)content.LongLength; } }
    public string SHA256 { get; }
    public byte[] GetBytes() { return (byte[])content.Clone(); }
}

public sealed class AgentDCAppPersonaSnapshot
{
    private readonly AgentDCAppPersonaSnapshotFile[] files;
    private readonly string[] identities;
    private readonly string[] protectedRootIdentities;

    public AgentDCAppPersonaSnapshot(
        string sourcePath,
        string sourceIdentity,
        string displayName,
        string[] identities,
        AgentDCAppPersonaSnapshotFile[] files,
        string treeSHA256,
        string[] protectedRootIdentities,
        string boundOutPath,
        string boundOutIdentity)
    {
        SourcePath = sourcePath ?? throw new ArgumentNullException(nameof(sourcePath));
        SourceIdentity = sourceIdentity ?? throw new ArgumentNullException(nameof(sourceIdentity));
        DisplayName = displayName ?? throw new ArgumentNullException(nameof(displayName));
        this.identities = identities == null ? throw new ArgumentNullException(nameof(identities)) :
            (string[])identities.Clone();
        this.files = files == null ? throw new ArgumentNullException(nameof(files)) :
            (AgentDCAppPersonaSnapshotFile[])files.Clone();
        TreeSHA256 = treeSHA256 ?? throw new ArgumentNullException(nameof(treeSHA256));
        this.protectedRootIdentities = protectedRootIdentities == null ? Array.Empty<string>() :
            (string[])protectedRootIdentities.Clone();
        BoundOutPath = boundOutPath;
        BoundOutIdentity = boundOutIdentity;
    }

    public string SourcePath { get; }
    public string SourceIdentity { get; }
    public string DisplayName { get; }
    public string TreeSHA256 { get; }
    public string BoundOutPath { get; }
    public string BoundOutIdentity { get; }
    public string[] GetIdentities() { return (string[])identities.Clone(); }
    public AgentDCAppPersonaSnapshotFile[] GetFiles() {
        return (AgentDCAppPersonaSnapshotFile[])files.Clone();
    }
    public string[] GetProtectedRootIdentities() {
        return (string[])protectedRootIdentities.Clone();
    }
}

public static class AgentDCAppPersonaFileReader
{
    public static byte[] ReadBounded(string path, long maximumBytes)
    {
        if (maximumBytes < 0) throw new ArgumentOutOfRangeException(nameof(maximumBytes));
        using (FileStream stream = new FileStream(
            path, FileMode.Open, FileAccess.Read, FileShare.Read, 64 * 1024,
            FileOptions.SequentialScan))
        {
            long length = stream.Length;
            if (length < 0 || length > maximumBytes) throw new IOException("file exceeds bound");
            byte[] result = new byte[(int)length];
            int offset = 0;
            while (offset < result.Length)
            {
                int read = stream.Read(result, offset, result.Length - offset);
                if (read == 0) throw new EndOfStreamException("file changed while reading");
                offset += read;
            }
            if (stream.ReadByte() != -1 || stream.Length != length)
                throw new IOException("file changed while reading");
            return result;
        }
    }
}
'@
}

$script:AppPersonaMaxIdentityBytes = 256
$script:AppPersonaMaxFileBytes = 1MB
$script:AppPersonaMaxWorkingPersonaBytes = 400KB
$script:AppPersonaMaxTreeBytes = 4MB
$script:AppPersonaMaxFiles = 64
$script:AppPersonaMaxRelativePathBytes = 512
$script:AppPersonaLegacySHA256 = '8c6ca4f7aa19096a0619c706286f7cff97452c3c4025c03ab59b083895bbacf2'
$script:AppPersonaStrictUTF8 = [Text.UTF8Encoding]::new($false, $true)
$script:AppPersonaKnownIdentityMarkers = @(
  'MIDU'
  'MenaQ7'
  'boizdeeptry'
  'Anh Trường'
  'Bé Mi'
)
$script:AppPersonaSensitiveMarkers = @(
  'APP_TEST_'
  'ONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91'
  'ONBOARDING_USER_PROMPT_CANARY_CLEAR_8172'
  'ONBOARDING_ANSWER_CANARY_CLEAR_BC43'
  'ONBOARDING_CREDENTIAL_CANARY_CLEAR_D912'
  'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E'
  'sk-package-must-never-contain-7f36d2'
)

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
  if (-not (Test-Path -LiteralPath $sourcePath -PathType Container -ErrorAction Stop)) {
    throw "nguồn persona không phải thư mục: '$sourcePath'"
  }
  try {
    Assert-AppPathHasNoReparsePoint -Path $sourcePath -InspectExistingTree | Out-Null
  } catch {
    throw 'persona source contains a reparse point or could not be inspected safely'
  }
  $required = @('identity.json', 'persona.md')
  foreach ($name in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourcePath $name) -PathType Leaf)) {
      throw "nguồn persona thiếu file '$name' trong '$sourcePath'"
    }
  }

  return $sourcePath
}

function Get-AppPersonaSHA256 {
  param([Parameter(Mandatory)][byte[]]$Bytes)

  return [Convert]::ToHexString(
    [Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}

function ConvertFrom-AppPersonaUTF8 {
  param(
    [Parameter(Mandatory)][byte[]]$Bytes,
    [Parameter(Mandatory)][string]$Label
  )

  try {
    return [Text.UTF8Encoding]::new($false, $true).GetString($Bytes)
  } catch {
    throw "persona source contains invalid UTF-8 in '$Label'"
  }
}

function Get-AppPersonaPathUTF8Bytes {
  param([Parameter(Mandatory)][string]$Path)

  try {
    return ,$script:AppPersonaStrictUTF8.GetBytes($Path)
  } catch {
    throw 'persona source path is invalid'
  }
}

function Assert-AppPersonaRelativePaths {
  param(
    [Parameter(Mandatory)][string[]]$RelativePath,
    [string[]]$Identity = @()
  )

  $caseFoldedPaths = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::OrdinalIgnoreCase)
  foreach ($relative in $RelativePath) {
    $pathBytes = Get-AppPersonaPathUTF8Bytes -Path $relative
    if ($pathBytes.Length -gt $script:AppPersonaMaxRelativePathBytes) {
      throw 'persona source path exceeds its bound'
    }
    if (-not $relative.IsNormalized([Text.NormalizationForm]::FormC)) {
      throw 'persona source path is not NFC-normalized'
    }
    foreach ($character in $relative.ToCharArray()) {
      if ([char]::IsControl($character)) {
        throw 'persona source path contains a control character'
      }
    }
    foreach ($component in $relative.Split(
        [char[]]@('/'), [StringSplitOptions]::None)) {
      if ([string]::IsNullOrEmpty($component) -or
          $component -ceq '.' -or $component -ceq '..' -or
          $component -cne $component.Trim()) {
        throw 'persona source path contains an invalid component'
      }
    }
    if (-not $caseFoldedPaths.Add($relative)) {
      throw 'persona source contains case-folded duplicate paths'
    }
    foreach ($marker in $script:AppPersonaSensitiveMarkers) {
      if ($relative.IndexOf($marker, [StringComparison]::Ordinal) -ge 0) {
        throw 'persona source privacy policy rejected a relative path'
      }
    }
    foreach ($value in $Identity) {
      if ($relative.IndexOf($value, [StringComparison]::Ordinal) -ge 0) {
        throw 'persona source privacy policy rejected a relative path'
      }
    }
  }
}

function Test-AppPersonaBytesContain {
  param(
    [Parameter(Mandatory)][byte[]]$Bytes,
    [Parameter(Mandatory)][byte[]]$Needle
  )

  if ($Needle.Length -eq 0 -or $Bytes.Length -lt $Needle.Length) { return $false }
  $last = $Bytes.Length - $Needle.Length
  for ($offset = 0; $offset -le $last; $offset++) {
    if ($Bytes[$offset] -ne $Needle[0]) { continue }
    $matches = $true
    for ($index = 1; $index -lt $Needle.Length; $index++) {
      if ($Bytes[$offset + $index] -ne $Needle[$index]) {
        $matches = $false
        break
      }
    }
    if ($matches) { return $true }
  }
  return $false
}

function Test-AppPersonaBytesContainLiteral {
  param(
    [Parameter(Mandatory)][byte[]]$Bytes,
    [Parameter(Mandatory)][string]$Literal
  )

  foreach ($encoding in @(
      [Text.UTF8Encoding]::new($false),
      [Text.UnicodeEncoding]::new($false, $false),
      [Text.UnicodeEncoding]::new($true, $false)
    )) {
    if (Test-AppPersonaBytesContain -Bytes $Bytes -Needle $encoding.GetBytes($Literal)) {
      return $true
    }
  }
  return $false
}

function Get-AppPersonaCodePointCount {
  param([Parameter(Mandatory)][string]$Value)

  $count = 0
  for ($index = 0; $index -lt $Value.Length; $index++) {
    $current = $Value[$index]
    if ([char]::IsHighSurrogate($current)) {
      if ($index + 1 -ge $Value.Length -or -not [char]::IsLowSurrogate($Value[$index + 1])) {
        throw 'persona identity metadata is invalid'
      }
      $index++
    } elseif ([char]::IsLowSurrogate($current)) {
      throw 'persona identity metadata is invalid'
    }
    $count++
  }
  return $count
}

function Assert-AppPersonaIdentityValue {
  param([Parameter(Mandatory)][string]$Value)

  if ([string]::IsNullOrWhiteSpace($Value) -or
      -not $Value.Equals($Value.Trim(), [StringComparison]::Ordinal) -or
      -not $Value.Equals($Value.Normalize([Text.NormalizationForm]::FormC), [StringComparison]::Ordinal) -or
      [Text.UTF8Encoding]::new($false).GetByteCount($Value) -gt $script:AppPersonaMaxIdentityBytes) {
    throw 'persona identity metadata is invalid'
  }
  if ((Get-AppPersonaCodePointCount $Value) -gt 60 -or
      $Value.Contains("`r") -or $Value.Contains("`n") -or
      $Value.Contains('{{') -or $Value.Contains('}}')) {
    throw 'persona identity metadata is invalid'
  }
  foreach ($character in $Value.ToCharArray()) {
    if ([char]::IsControl($character)) {
      throw 'persona identity metadata is invalid'
    }
  }
  return $Value
}

function Get-AppPersonaStrictProperties {
  param(
    [Parameter(Mandatory)][Text.Json.JsonElement]$Element,
    [Parameter(Mandatory)][string[]]$Names
  )

  if ($Element.ValueKind -ne [Text.Json.JsonValueKind]::Object) {
    throw 'persona identity metadata is invalid'
  }
  $allowed = [Collections.Generic.HashSet[string]]::new(
    [string[]]$Names, [StringComparer]::Ordinal)
  $properties = [Collections.Generic.Dictionary[string, Text.Json.JsonElement]]::new(
    [StringComparer]::Ordinal)
  foreach ($property in $Element.EnumerateObject()) {
    if (-not $allowed.Contains($property.Name) -or
        -not $properties.TryAdd($property.Name, $property.Value.Clone())) {
      throw 'persona identity metadata is invalid'
    }
  }
  foreach ($name in $Names) {
    if (-not $properties.ContainsKey($name)) {
      throw 'persona identity metadata is invalid'
    }
  }
  return ,$properties
}

function Read-AppPersonaIdentityMetadata {
  param([Parameter(Mandatory)][byte[]]$Bytes)

  $text = ConvertFrom-AppPersonaUTF8 -Bytes $Bytes -Label 'identity.json'
  $options = [Text.Json.JsonDocumentOptions]::new()
  $options.AllowTrailingCommas = $false
  $options.CommentHandling = [Text.Json.JsonCommentHandling]::Disallow
  $options.MaxDepth = 8
  try {
    $document = [Text.Json.JsonDocument]::Parse($text, $options)
  } catch {
    throw 'persona identity metadata is invalid'
  }
  try {
    $root = Get-AppPersonaStrictProperties -Element $document.RootElement `
      -Names @('version', 'display_name', 'persona_identities')
    if ($root['version'].ValueKind -ne [Text.Json.JsonValueKind]::Number) {
      throw 'persona identity metadata is invalid'
    }
    $version = 0
    if (-not $root['version'].TryGetInt32([ref]$version) -or $version -ne 1 -or
        $root['display_name'].ValueKind -ne [Text.Json.JsonValueKind]::String -or
        $root['persona_identities'].ValueKind -ne [Text.Json.JsonValueKind]::Array) {
      throw 'persona identity metadata is invalid'
    }
    $displayName = Assert-AppPersonaIdentityValue -Value $root['display_name'].GetString()
    $identityElements = @($root['persona_identities'].EnumerateArray())
    if ($identityElements.Count -lt 1 -or $identityElements.Count -gt 8) {
      throw 'persona identity metadata is invalid'
    }
    $identities = [Collections.Generic.List[string]]::new()
    $unique = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $assistant = $null
    $assistantCount = 0
    foreach ($element in $identityElements) {
      $entry = Get-AppPersonaStrictProperties -Element $element -Names @('role', 'value')
      if ($entry['role'].ValueKind -ne [Text.Json.JsonValueKind]::String -or
          $entry['value'].ValueKind -ne [Text.Json.JsonValueKind]::String) {
        throw 'persona identity metadata is invalid'
      }
      $role = $entry['role'].GetString()
      if ($role -cnotin @('assistant', 'expert', 'business')) {
        throw 'persona identity metadata is invalid'
      }
      $value = Assert-AppPersonaIdentityValue -Value $entry['value'].GetString()
      if (-not $unique.Add($value)) {
        throw 'persona identity metadata is invalid'
      }
      if ($role -ceq 'assistant') {
        $assistant = $value
        $assistantCount++
      }
      $identities.Add($value)
    }
    if ($assistantCount -ne 1 -or
        -not $displayName.Equals($assistant, [StringComparison]::Ordinal)) {
      throw 'persona identity metadata is invalid'
    }
    return [PSCustomObject]@{
      DisplayName = $displayName
      Identities = [string[]]$identities.ToArray()
    }
  } catch {
    if ($_.Exception.Message -eq 'persona identity metadata is invalid') { throw }
    throw 'persona identity metadata is invalid'
  } finally {
    $document.Dispose()
  }
}

function Assert-AppPersonaTextComplete {
  param(
    [Parameter(Mandatory)][string]$Text,
    [Parameter(Mandatory)][string]$Label
  )

  if ($Text.Contains('{{') -or $Text.Contains('}}')) {
    throw "persona source is incomplete in '$Label'"
  }
  $depth = 0
  foreach ($character in $Text.ToCharArray()) {
    if ($character -eq '{') {
      $depth++
    } elseif ($character -eq '}') {
      $depth--
      if ($depth -lt 0) { throw "persona source has unmatched braces in '$Label'" }
    }
  }
  if ($depth -ne 0) { throw "persona source has unmatched braces in '$Label'" }
}

function Get-AppPersonaUInt64BigEndian {
  param([Parameter(Mandatory)][UInt64]$Value)

  $bytes = [BitConverter]::GetBytes($Value)
  if ([BitConverter]::IsLittleEndian) { [Array]::Reverse($bytes) }
  return $bytes
}

function Get-AppPersonaTreeSHA256 {
  param([Parameter(Mandatory)][AgentDCAppPersonaSnapshotFile[]]$Files)

  $hash = [Security.Cryptography.IncrementalHash]::CreateHash(
    [Security.Cryptography.HashAlgorithmName]::SHA256)
  try {
    $hash.AppendData($script:AppPersonaStrictUTF8.GetBytes("agentdc/persona-default-tree/v1`0"))
    foreach ($file in $Files) {
      $pathBytes = Get-AppPersonaPathUTF8Bytes -Path $file.Path
      $content = $file.GetBytes()
      $hash.AppendData((Get-AppPersonaUInt64BigEndian ([UInt64]$pathBytes.Length)))
      $hash.AppendData($pathBytes)
      $hash.AppendData((Get-AppPersonaUInt64BigEndian ([UInt64]$content.Length)))
      $hash.AppendData($content)
    }
    return [Convert]::ToHexString($hash.GetHashAndReset()).ToLowerInvariant()
  } finally {
    $hash.Dispose()
  }
}

function Assert-AppPersonaOutputSeparation {
  param(
    [Parameter(Mandatory)][string]$Out,
    [Parameter(Mandatory)][string[]]$ProtectedIdentity,
    [string]$ExpectedIdentity
  )

  $outPath = [IO.Path]::GetFullPath($Out)
  Assert-AppPathHasNoReparsePoint -Path $outPath -InspectExistingTree | Out-Null
  $outIdentity = Get-AppPhysicalPathIdentity -Path $outPath
  if ($ExpectedIdentity -and
      -not $outIdentity.Equals($ExpectedIdentity, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'persona output physical identity changed after preflight'
  }
  foreach ($protected in $ProtectedIdentity) {
    if (Test-AppPhysicalPathsOverlap -Left $outIdentity -Right $protected) {
      throw 'persona output overlaps a protected root'
    }
  }
  return [PSCustomObject]@{ Path = $outPath; Identity = $outIdentity }
}

function Read-AppPersonaSourceFileBytes {
  param(
    [Parameter(Mandatory)][string]$FullPath,
    [Parameter(Mandatory)][string]$SourceIdentity
  )

  try {
    Assert-AppPathHasNoReparsePoint -Path $FullPath | Out-Null
    $beforeIdentity = Get-AppPhysicalPathIdentity -Path $FullPath
    $sourcePrefix = $SourceIdentity.TrimEnd('\', '/') + '\'
    if (-not $beforeIdentity.StartsWith($sourcePrefix, [StringComparison]::OrdinalIgnoreCase)) {
      throw 'source file escaped its root'
    }
    $bytes = [AgentDCAppPersonaFileReader]::ReadBounded(
      $FullPath, $script:AppPersonaMaxFileBytes)
    $afterIdentity = Get-AppPhysicalPathIdentity -Path $FullPath
    Assert-AppPathHasNoReparsePoint -Path $FullPath | Out-Null
    if (-not $beforeIdentity.Equals($afterIdentity, [StringComparison]::OrdinalIgnoreCase) -or
        -not $afterIdentity.StartsWith($sourcePrefix, [StringComparison]::OrdinalIgnoreCase)) {
      throw 'source file identity changed during snapshot'
    }
    return ,$bytes
  } catch {
    throw 'persona source file could not be snapshotted'
  }
}

function Read-AppPersonaSourceSnapshot {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$PersonaSource,
    [string]$Out,
    [string[]]$ProtectedRoot = @()
  )

  $sourcePath = Resolve-PersonaSource -PersonaSource $PersonaSource
  $sourceIdentity = Get-AppPhysicalPathIdentity -Path $sourcePath
  $protectedIdentities = [Collections.Generic.List[string]]::new()
  $protectedIdentities.Add($sourceIdentity)
  foreach ($root in $ProtectedRoot) {
    if ([string]::IsNullOrWhiteSpace($root)) { throw 'persona protected root is invalid' }
    $protectedIdentities.Add((Get-AppPhysicalPathIdentity -Path $root))
  }
  $boundOutPath = $null
  $boundOutIdentity = $null
  if (-not [string]::IsNullOrWhiteSpace($Out)) {
    $separation = Assert-AppPersonaOutputSeparation -Out $Out `
      -ProtectedIdentity $protectedIdentities.ToArray()
    $boundOutPath = $separation.Path
    $boundOutIdentity = $separation.Identity
  }

  $sourceFiles = [Collections.Generic.Dictionary[string, string]]::new([StringComparer]::Ordinal)
  try {
    foreach ($item in @(Get-ChildItem -LiteralPath $sourcePath -Force -ErrorAction Stop)) {
      if ($item.Name -ceq 'overlay') {
        if (-not $item.PSIsContainer) { throw 'invalid overlay' }
        foreach ($overlayFile in @(Get-ChildItem -LiteralPath $item.FullName -Force -Recurse -File -ErrorAction Stop)) {
          $relative = [IO.Path]::GetRelativePath($sourcePath, $overlayFile.FullName).Replace('\', '/')
          if (-not $relative.StartsWith('overlay/', [StringComparison]::Ordinal)) {
            throw 'invalid overlay path'
          }
          if ($relative.EndsWith('.goc', [StringComparison]::OrdinalIgnoreCase)) {
            throw 'overlay backup is not build input'
          }
          if (-not $sourceFiles.TryAdd($relative, $overlayFile.FullName)) {
            throw 'duplicate paths'
          }
        }
        continue
      }
      if ($item.Name -ceq 'persona.md.goc') {
        if ($item.PSIsContainer -or $item.Length -gt $script:AppPersonaMaxFileBytes) {
          throw 'invalid operator backup'
        }
        continue
      }
      if ($item.Name -cnotin @('identity.json', 'persona.md', 'roster.md') -or $item.PSIsContainer) {
        throw 'unknown entry'
      }
      if (-not $sourceFiles.TryAdd($item.Name, $item.FullName)) {
        throw 'duplicate paths'
      }
    }
  } catch {
    throw 'persona source tree could not be enumerated safely'
  }
  foreach ($required in @('identity.json', 'persona.md')) {
    if (-not $sourceFiles.ContainsKey($required)) {
      throw 'persona source is incomplete'
    }
  }
  if ($sourceFiles.Count -gt $script:AppPersonaMaxFiles) {
    throw 'persona source contains too many files'
  }

  $relativePaths = [string[]]@($sourceFiles.Keys)
  [Array]::Sort($relativePaths, [StringComparer]::Ordinal)
  Assert-AppPersonaRelativePaths -RelativePath $relativePaths `
    -Identity $script:AppPersonaKnownIdentityMarkers

  $identityBytes = Read-AppPersonaSourceFileBytes `
    -FullPath $sourceFiles['identity.json'] -SourceIdentity $sourceIdentity
  foreach ($marker in $script:AppPersonaSensitiveMarkers) {
    if (Test-AppPersonaBytesContainLiteral -Bytes $identityBytes -Literal $marker) {
      throw 'persona source privacy policy rejected identity metadata'
    }
  }
  $metadata = Read-AppPersonaIdentityMetadata -Bytes $identityBytes
  Assert-AppPersonaRelativePaths -RelativePath $relativePaths -Identity $metadata.Identities
  $declared = [Collections.Generic.HashSet[string]]::new(
    [string[]]$metadata.Identities, [StringComparer]::Ordinal)

  $snapshotFiles = [Collections.Generic.List[AgentDCAppPersonaSnapshotFile]]::new()
  $decodedPersona = [Collections.Generic.List[string]]::new()
  $treeBytes = [UInt64]0
  foreach ($relative in $relativePaths) {
    $bytes = if ($relative -ceq 'identity.json') {
      $identityBytes
    } else {
      Read-AppPersonaSourceFileBytes -FullPath $sourceFiles[$relative] `
        -SourceIdentity $sourceIdentity
    }
    $treeBytes += [UInt64]$bytes.Length
    if ($treeBytes -gt [UInt64]$script:AppPersonaMaxTreeBytes) {
      throw 'persona source tree exceeds its bound'
    }
    foreach ($marker in $script:AppPersonaSensitiveMarkers) {
      if (Test-AppPersonaBytesContainLiteral -Bytes $bytes -Literal $marker) {
        throw "persona source privacy policy rejected '$relative'"
      }
    }
    $text = ConvertFrom-AppPersonaUTF8 -Bytes $bytes -Label $relative
    if ($relative -ceq 'persona.md' -and
        ($bytes.Length -gt $script:AppPersonaMaxWorkingPersonaBytes -or
         [string]::IsNullOrWhiteSpace($text))) {
      throw 'persona source persona.md is blank or exceeds 400 KiB'
    }
    if ($relative -cne 'identity.json') {
      Assert-AppPersonaTextComplete -Text $text -Label $relative
      $decodedPersona.Add($text)
    }
    $snapshotFiles.Add([AgentDCAppPersonaSnapshotFile]::new(
      $relative, $bytes, (Get-AppPersonaSHA256 $bytes)))
  }

  $personaText = $decodedPersona -join "`n"
  foreach ($identity in $metadata.Identities) {
    if ($personaText.IndexOf($identity, [StringComparison]::Ordinal) -lt 0) {
      throw 'persona identity metadata declares a value absent from Persona content'
    }
  }
  foreach ($knownIdentity in $script:AppPersonaKnownIdentityMarkers) {
    if ($declared.Contains($knownIdentity)) { continue }
    foreach ($file in $snapshotFiles) {
      if (Test-AppPersonaBytesContainLiteral -Bytes $file.GetBytes() -Literal $knownIdentity) {
        throw "persona source privacy policy rejected '$($file.Path)'"
      }
    }
  }
  $files = [AgentDCAppPersonaSnapshotFile[]]$snapshotFiles.ToArray()
  $treeSHA256 = Get-AppPersonaTreeSHA256 -Files $files
  return [AgentDCAppPersonaSnapshot]::new(
    $sourcePath,
    $sourceIdentity,
    $metadata.DisplayName,
    [string[]]$metadata.Identities,
    $files,
    $treeSHA256,
    [string[]]($protectedIdentities | Select-Object -Skip 1),
    $boundOutPath,
    $boundOutIdentity)
}

function Get-AppPersonaManifestBytes {
  param([Parameter(Mandatory)][AgentDCAppPersonaSnapshot]$Snapshot)

  $entries = [Collections.Generic.List[string]]::new()
  foreach ($file in $Snapshot.GetFiles()) {
    $jsonPath = '"' + [Text.Json.JsonEncodedText]::Encode([string]$file.Path).ToString() + '"'
    $entries.Add('{"path":' + $jsonPath + ',"bytes":' +
      $file.Length.ToString([Globalization.CultureInfo]::InvariantCulture) +
      ',"sha256":"' + $file.SHA256 + '"}')
  }
  $json = '{"version":1,"files":[' + ($entries -join ',') +
    '],"tree_sha256":"' + $Snapshot.TreeSHA256 +
    '","legacy_persona_sha256":["' + $script:AppPersonaLegacySHA256 + '"]}' + "`n"
  return [Text.UTF8Encoding]::new($false).GetBytes($json)
}

function Assert-AppPersonaSnapshotType {
  param([Parameter(Mandatory)]$Snapshot)

  if ($Snapshot -isnot [AgentDCAppPersonaSnapshot]) {
    throw 'persona snapshot type is invalid'
  }
}

function Write-AppPersonaPackage {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)]$Snapshot,
    [Parameter(Mandatory)][string]$Out
  )

  Assert-AppPersonaSnapshotType -Snapshot $Snapshot
  $outPath = [IO.Path]::GetFullPath($Out)
  if ($Snapshot.BoundOutPath -and
      -not $outPath.Equals($Snapshot.BoundOutPath, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'persona snapshot is bound to a different output path'
  }
  $protected = [Collections.Generic.List[string]]::new()
  $protected.Add($Snapshot.SourceIdentity)
  foreach ($identity in $Snapshot.GetProtectedRootIdentities()) { $protected.Add($identity) }
  $separation = Assert-AppPersonaOutputSeparation -Out $outPath `
    -ProtectedIdentity $protected.ToArray() -ExpectedIdentity $Snapshot.BoundOutIdentity
  $outPath = $separation.Path

  $workingRoot = Join-Path $outPath 'brain\reference\persona'
  $defaultRoot = Join-Path $outPath 'app\defaults\persona'
  foreach ($root in @($workingRoot, $defaultRoot)) {
    [IO.Directory]::CreateDirectory($root) | Out-Null
  }
  foreach ($file in $Snapshot.GetFiles()) {
    $platformRelative = $file.Path.Replace('/', [IO.Path]::DirectorySeparatorChar)
    foreach ($root in @($workingRoot, $defaultRoot)) {
      $destination = Join-Path $root $platformRelative
      [IO.Directory]::CreateDirectory((Split-Path -Parent $destination)) | Out-Null
      Assert-AppPathHasNoReparsePoint -Path $destination | Out-Null
      [IO.File]::WriteAllBytes($destination, $file.GetBytes())
      $written = [AgentDCAppPersonaFileReader]::ReadBounded(
        $destination, $script:AppPersonaMaxFileBytes)
      if ((Get-AppPersonaSHA256 $written) -cne $file.SHA256 -or
          [UInt64]$written.Length -ne $file.Length) {
        throw "persona destination verification failed for '$($file.Path)'"
      }
    }
  }
  $manifestPath = Join-Path $defaultRoot 'build-manifest.json'
  $manifestBytes = Get-AppPersonaManifestBytes -Snapshot $Snapshot
  Assert-AppPathHasNoReparsePoint -Path $manifestPath | Out-Null
  [IO.File]::WriteAllBytes($manifestPath, $manifestBytes)
  $manifestWritten = [AgentDCAppPersonaFileReader]::ReadBounded(
    $manifestPath, $script:AppPersonaMaxFileBytes)
  if (-not [Linq.Enumerable]::SequenceEqual(
      [byte[]]$manifestBytes, [byte[]]$manifestWritten)) {
    throw 'persona manifest destination verification failed'
  }
}

function Get-AppPersonaEncodedNeedles {
  param([Parameter(Mandatory)][string[]]$Values)

  $needles = [Collections.Generic.List[byte[]]]::new()
  $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
  foreach ($value in $Values) {
    if ([string]::IsNullOrEmpty($value) -or -not $seen.Add($value)) { continue }
    foreach ($encoding in @(
        [Text.UTF8Encoding]::new($false),
        [Text.UnicodeEncoding]::new($false, $false),
        [Text.UnicodeEncoding]::new($true, $false)
      )) {
      $needles.Add($encoding.GetBytes($value))
    }
  }
  return ,$needles.ToArray()
}

function Assert-AppPersonaPackagePrivacy {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Out,
    [Parameter(Mandatory)]$Snapshot
  )

  Assert-AppPersonaSnapshotType -Snapshot $Snapshot
  $outPath = [IO.Path]::GetFullPath($Out)
  try {
    Assert-AppPathHasNoReparsePoint -Path $outPath -InspectExistingTree | Out-Null
    $entries = @(Get-AppSafePackageEntries -Root $outPath)
  } catch {
    throw 'persona package privacy policy could not inspect package entries safely'
  }
  $files = @($entries | Where-Object { -not $_.PSIsContainer })
  $pathForbidden = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
  foreach ($marker in $script:AppPersonaSensitiveMarkers) {
    $pathForbidden.Add($marker) | Out-Null
  }
  foreach ($identity in $Snapshot.GetIdentities()) {
    $pathForbidden.Add($identity) | Out-Null
  }
  foreach ($known in $script:AppPersonaKnownIdentityMarkers) {
    $pathForbidden.Add($known) | Out-Null
  }
  foreach ($entry in $entries) {
    try {
      $relative = [IO.Path]::GetRelativePath($outPath, $entry.FullName).Replace('\', '/')
      $normalizedRelative = $relative.Normalize([Text.NormalizationForm]::FormC)
    } catch {
      throw 'persona package privacy policy rejected an entry path'
    }
    foreach ($forbidden in $pathForbidden) {
      if ($normalizedRelative.IndexOf($forbidden, [StringComparison]::Ordinal) -ge 0) {
        throw 'persona package privacy policy rejected an entry path'
      }
    }
  }
  $workingPrefix = 'brain/reference/persona/'
  $defaultPrefix = 'app/defaults/persona/'
  $workingRoot = $workingPrefix.TrimEnd('/')
  $defaultRoot = $defaultPrefix.TrimEnd('/')
  $workingBackupRelative = 'brain/reference/persona/persona.md.goc'
  $manifestRelative = 'app/defaults/persona/build-manifest.json'
  $allowedPersonaFiles = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
  $expectedPersonaFiles = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
  $expectedPersonaDirectories = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::Ordinal)
  $expectedPersonaDirectories.Add($workingRoot) | Out-Null
  $expectedPersonaDirectories.Add($defaultRoot) | Out-Null
  $expectedPersonaDirectories.Add($workingPrefix + 'overlay') | Out-Null
  foreach ($snapshotFile in $Snapshot.GetFiles()) {
    foreach ($prefix in @($workingPrefix, $defaultPrefix)) {
      $relative = $prefix + $snapshotFile.Path
      $allowedPersonaFiles.Add($relative) | Out-Null
      $expectedPersonaFiles.Add($relative) | Out-Null
      $destination = Join-Path $outPath $relative.Replace('/', '\')
      if (-not (Test-Path -LiteralPath $destination -PathType Leaf -ErrorAction Stop)) {
        throw "persona package is missing '$relative'"
      }
      $bytes = [AgentDCAppPersonaFileReader]::ReadBounded(
        $destination, $script:AppPersonaMaxFileBytes)
      if ((Get-AppPersonaSHA256 $bytes) -cne $snapshotFile.SHA256 -or
          [UInt64]$bytes.Length -ne $snapshotFile.Length) {
        throw "persona package verification failed for '$relative'"
      }
    }
    $directory = [IO.Path]::GetDirectoryName($snapshotFile.Path.Replace('/', '\'))
    while (-not [string]::IsNullOrEmpty($directory)) {
      $relativeDirectory = $directory.Replace('\', '/')
      $expectedPersonaDirectories.Add($workingPrefix + $relativeDirectory) | Out-Null
      $expectedPersonaDirectories.Add($defaultPrefix + $relativeDirectory) | Out-Null
      $directory = [IO.Path]::GetDirectoryName($directory)
    }
  }
  $expectedPersonaFiles.Add($manifestRelative) | Out-Null
  $manifestPath = Join-Path $outPath $manifestRelative.Replace('/', '\')
  try {
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf -ErrorAction Stop)) {
      throw 'missing manifest'
    }
    $manifestActual = [AgentDCAppPersonaFileReader]::ReadBounded(
      $manifestPath, $script:AppPersonaMaxFileBytes)
  } catch {
    throw 'persona package manifest verification failed'
  }
  if (-not [Linq.Enumerable]::SequenceEqual(
      [byte[]](Get-AppPersonaManifestBytes -Snapshot $Snapshot),
      [byte[]]$manifestActual)) {
    throw 'persona package manifest verification failed'
  }

  foreach ($item in $entries) {
    $relative = [IO.Path]::GetRelativePath($outPath, $item.FullName).Replace('\', '/')
    if ($item.PSIsContainer -and
        ($relative.Equals($workingRoot, [StringComparison]::Ordinal) -or
         $relative.StartsWith($workingPrefix, [StringComparison]::Ordinal) -or
         $relative.Equals($defaultRoot, [StringComparison]::Ordinal) -or
         $relative.StartsWith($defaultPrefix, [StringComparison]::Ordinal)) -and
        -not $expectedPersonaDirectories.Contains($relative)) {
      throw 'persona package privacy policy rejected an unexpected Persona directory'
    }
    if ($item.Name.EndsWith('.goc', [StringComparison]::OrdinalIgnoreCase)) {
      if (-not $relative.Equals($workingBackupRelative, [StringComparison]::Ordinal) -or
          $item.PSIsContainer -or
          ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'persona package privacy policy rejected a backup entry'
      }
      $defaultPersona = Join-Path $outPath 'app\defaults\persona\persona.md'
      try {
        $backupBytes = [AgentDCAppPersonaFileReader]::ReadBounded(
          $item.FullName, $script:AppPersonaMaxFileBytes)
        $defaultBytes = [AgentDCAppPersonaFileReader]::ReadBounded(
          $defaultPersona, $script:AppPersonaMaxFileBytes)
      } catch {
        throw 'persona package privacy policy rejected the working backup'
      }
      if (-not [Linq.Enumerable]::SequenceEqual(
          [byte[]]$backupBytes, [byte[]]$defaultBytes)) {
        throw 'persona package privacy policy rejected the working backup'
      }
    }
    if (-not $item.PSIsContainer -and
        ($relative.StartsWith($workingPrefix, [StringComparison]::Ordinal) -or
         $relative.StartsWith($defaultPrefix, [StringComparison]::Ordinal)) -and
        -not $expectedPersonaFiles.Contains($relative) -and
        -not $relative.Equals($workingBackupRelative, [StringComparison]::Ordinal)) {
      throw "persona package privacy policy rejected '$relative'"
    }
  }

  $declared = [Collections.Generic.HashSet[string]]::new(
    [string[]]$Snapshot.GetIdentities(), [StringComparer]::Ordinal)
  foreach ($file in $files) {
    $relative = [IO.Path]::GetRelativePath($outPath, $file.FullName).Replace('\', '/')
    $forbidden = [Collections.Generic.List[string]]::new()
    foreach ($marker in $script:AppPersonaSensitiveMarkers) { $forbidden.Add($marker) }
    $identityAllowed = $allowedPersonaFiles.Contains($relative) -or
      $relative.Equals($workingBackupRelative, [StringComparison]::Ordinal)
    if (-not $identityAllowed) {
      foreach ($identity in $Snapshot.GetIdentities()) { $forbidden.Add($identity) }
    }
    foreach ($known in $script:AppPersonaKnownIdentityMarkers) {
      if (-not $declared.Contains($known)) { $forbidden.Add($known) }
    }
    $needles = Get-AppPersonaEncodedNeedles -Values $forbidden.ToArray()
    try {
      $found = [AgentDCAppPackageByteScanner]::ContainsAny($file.FullName, $needles)
    } catch {
      throw "persona package privacy scan failed for '$relative'"
    }
    if ($found) {
      throw "persona package privacy policy rejected '$relative'"
    }
  }
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

func productionRuntimeRunner(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "appZaloRunner" {
		return false
	}
	contextCall, ok := sel.X.(*ast.CallExpr)
	if !ok || len(contextCall.Args) != 1 {
		return false
	}
	contextFactory, ok := contextCall.Fun.(*ast.Ident)
	if !ok || contextFactory.Name != "productionAppRuntimeContext" {
		return false
	}
	apiArg, ok := contextCall.Args[0].(*ast.Ident)
	return ok && apiArg.Name == "a"
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
	if !funOK || fun.Name != "route" || len(call.Args) != 4 ||
		!selector(call.Args[0], "deps", "cfg") || !selector(call.Args[1], "deps", "run") ||
		!ident(call.Args[2], "threadID") || !exactRouteAttachmentFlag(call.Args[3]) {
		return token.NoPos, false
	}
	return call.Pos(), true
}

func exactRouteAttachmentFlag(expr ast.Expr) bool {
	comparison, ok := expr.(*ast.BinaryExpr)
	if !ok || comparison.Op != token.GTR {
		return false
	}
	zero, ok := comparison.Y.(*ast.BasicLit)
	if !ok || zero.Kind != token.INT || zero.Value != "0" {
		return false
	}
	length, ok := comparison.X.(*ast.CallExpr)
	return ok && ident(length.Fun, "len") && len(length.Args) == 1 && ident(length.Args[0], "files")
}

func directAcquire(node ast.Node) (token.Pos, bool) {
	assignment, ok := node.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 2 ||
		len(assignment.Rhs) != 1 || !ident(assignment.Lhs[0], "release") ||
		!ident(assignment.Lhs[1], "err") {
		return token.NoPos, false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if ok && selector(call.Fun, "appZaloProcessThreadGate", "Acquire") &&
		len(call.Args) == 2 && exactBackground(call.Args[0]) && exactThreadGateKey(call.Args[1]) {
		return call.Pos(), true
	}
	return token.NoPos, false
}

func exactBackground(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && selector(call.Fun, "context", "Background") && len(call.Args) == 0
}

func exactThreadGateKey(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || !selector(call.Fun, "fmt", "Sprintf") || len(call.Args) != 3 {
		return false
	}
	format, ok := call.Args[0].(*ast.BasicLit)
	return ok && format.Kind == token.STRING && format.Value == "\"%p\\x00%s\"" &&
		selector(call.Args[1], "a", "st") && ident(call.Args[2], "threadID")
}

func ident(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

func assignmentWrites(assignment *ast.AssignStmt, name string) bool {
	for _, left := range assignment.Lhs {
		if ident(left, name) {
			return true
		}
	}
	return false
}

func directAnswerFactoryCall(statement ast.Stmt) (*ast.CallExpr, bool) {
	var expression ast.Expr
	switch stmt := statement.(type) {
	case *ast.ExprStmt:
		expression = stmt.X
	case *ast.ReturnStmt:
		if len(stmt.Results) != 1 {
			return nil, false
		}
		expression = stmt.Results[0]
	default:
		return nil, false
	}
	call, ok := expression.(*ast.CallExpr)
	return call, ok && selector(call.Fun, "a", "appAnswerZaloWithRunnerFactory") &&
		len(call.Args) == 6 && ident(call.Args[0], "deps") && ident(call.Args[1], "threadID") &&
		ident(call.Args[2], "question") && ident(call.Args[3], "reply") &&
		ident(call.Args[4], "files") && productionRuntimeRunner(call.Args[5])
}

func directAnswerExecution(statement ast.Stmt) (*ast.CallExpr, bool) {
	stmt, ok := statement.(*ast.IfStmt)
	if !ok || stmt.Else != nil || len(stmt.Body.List) != 1 {
		return nil, false
	}
	assignment, ok := stmt.Init.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !ident(assignment.Lhs[0], "err") {
		return nil, false
	}
	condition, ok := stmt.Cond.(*ast.BinaryExpr)
	if !ok || condition.Op != token.NEQ || !ident(condition.X, "err") || !ident(condition.Y, "nil") {
		return nil, false
	}
	returned, ok := stmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(returned.Results) != 1 || !ident(returned.Results[0], "err") {
		return nil, false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	return call, ok && selector(call.Fun, "a", "answerZalo") && len(call.Args) == 8 &&
		ident(call.Args[0], "ctx") && selector(call.Args[1], "deps", "cfg") &&
		ident(call.Args[2], "run") && ident(call.Args[3], "threadID") &&
		ident(call.Args[4], "question") && ident(call.Args[5], "step") &&
		ident(call.Args[6], "reply") && ident(call.Args[7], "files") && call.Ellipsis.IsValid()
}

func deferredRelease(statement ast.Stmt) (token.Pos, bool) {
	deferred, ok := statement.(*ast.DeferStmt)
	if !ok || !ident(deferred.Call.Fun, "release") || len(deferred.Call.Args) != 0 {
		return token.NoPos, false
	}
	return deferred.Call.Pos(), true
}

func routeNilGuard(statement ast.Stmt) bool {
	guard, ok := statement.(*ast.IfStmt)
	if !ok || guard.Init != nil || guard.Else != nil || len(guard.Body.List) != 1 {
		return false
	}
	condition, ok := guard.Cond.(*ast.BinaryExpr)
	if !ok || condition.Op != token.EQL || !ident(condition.X, "route") ||
		!ident(condition.Y, "nil") {
		return false
	}
	returned, ok := guard.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(returned.Results) != 1 {
		return false
	}
	call, ok := returned.Results[0].(*ast.CallExpr)
	if !ok || !selector(call.Fun, "errors", "New") || len(call.Args) != 1 {
		return false
	}
	message, ok := call.Args[0].(*ast.BasicLit)
	return ok && message.Kind == token.STRING &&
		message.Value == "\"zalo session runner factory is required\""
}

func exactAssignment(assignment *ast.AssignStmt, left string) (ast.Expr, bool) {
	if assignment == nil || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !ident(assignment.Lhs[0], left) {
		return nil, false
	}
	return assignment.Rhs[0], true
}

func resolvedRouteAssignment(assignment *ast.AssignStmt) bool {
	if assignment == nil || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 4 ||
		len(assignment.Rhs) != 1 {
		return false
	}
	want := []string{"run", "effectiveConfig", "binding", "err"}
	for i, name := range want {
		if !ident(assignment.Lhs[i], name) {
			return false
		}
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	return ok && selector(call.Fun, "a", "appResolveZaloSessionRoute") &&
		len(call.Args) == 4 && ident(call.Args[0], "ctx") && ident(call.Args[1], "run") &&
		ident(call.Args[2], "effectiveConfig") && ident(call.Args[3], "threadID")
}

func effectiveConfigSource(assignment *ast.AssignStmt) bool {
	return assignment != nil && assignment.Tok == token.DEFINE && len(assignment.Lhs) == 1 &&
		len(assignment.Rhs) == 1 && ident(assignment.Lhs[0], "effectiveConfig") &&
		selector(assignment.Rhs[0], "deps", "cfg")
}

func turnSnapshotSource(assignment *ast.AssignStmt) bool {
	if assignment == nil || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !ident(assignment.Lhs[0], "snapshot") {
		return false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	return ok && selector(call.Fun, "a", "appCaptureZaloTurnSnapshot") && len(call.Args) == 3 &&
		ident(call.Args[0], "effectiveConfig") && ident(call.Args[1], "threadID") &&
		ident(call.Args[2], "files")
}

func expressionRootedAt(expr ast.Expr, name string) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name == name
	case *ast.SelectorExpr:
		return expressionRootedAt(value.X, name)
	case *ast.IndexExpr:
		return expressionRootedAt(value.X, name)
	case *ast.ParenExpr:
		return expressionRootedAt(value.X, name)
	}
	return false
}

func assignmentWritesRoot(assignment *ast.AssignStmt, name string) bool {
	for _, left := range assignment.Lhs {
		if expressionRootedAt(left, name) {
			return true
		}
	}
	return false
}

func typedRunBinding(stmt *ast.IfStmt, bound, interfaceName string) bool {
	assignment, ok := stmt.Init.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 2 ||
		len(assignment.Rhs) != 1 || !ident(assignment.Lhs[0], bound) ||
		!ident(assignment.Lhs[1], "ok") || !ident(stmt.Cond, "ok") {
		return false
	}
	assertion, ok := assignment.Rhs[0].(*ast.TypeAssertExpr)
	return ok && ident(assertion.X, "run") && ident(assertion.Type, interfaceName)
}

func awareRouteAssignment(stmt *ast.IfStmt, interfaceName, method string) (token.Pos, bool) {
	if !typedRunBinding(stmt, "aware", interfaceName) || stmt.Else != nil || len(stmt.Body.List) != 1 {
		return token.NoPos, false
	}
	assignment, ok := stmt.Body.List[0].(*ast.AssignStmt)
	right, okRight := exactAssignment(assignment, "run")
	if !ok || !okRight {
		return token.NoPos, false
	}
	call, ok := right.(*ast.CallExpr)
	if !ok || !selector(call.Fun, "aware", method) || len(call.Args) != 1 {
		return token.NoPos, false
	}
	switch method {
	case "appWithZaloAttachments":
		if !selector(call.Args[0], "snapshot", "hasAttachments") {
			return token.NoPos, false
		}
	case "appWithZaloStructuredClaudeRequirement":
		predicate, ok := call.Args[0].(*ast.CallExpr)
		if !ok || !ident(predicate.Fun, "appZaloDeltaHasAttachments") || len(predicate.Args) != 1 {
			return token.NoPos, false
		}
		delta, ok := predicate.Args[0].(*ast.SelectorExpr)
		if !ok || delta.Sel.Name != "delta" {
			return token.NoPos, false
		}
		resume, ok := delta.X.(*ast.SelectorExpr)
		if !ok || resume.Sel.Name != "resumeDelta" || !ident(resume.X, "snapshot") {
			return token.NoPos, false
		}
	default:
		return token.NoPos, false
	}
	return assignment.Pos(), true
}

func pointerComposite(expr ast.Expr, typeName string) (*ast.CompositeLit, bool) {
	address, ok := expr.(*ast.UnaryExpr)
	if !ok || address.Op != token.AND {
		return nil, false
	}
	literal, ok := address.X.(*ast.CompositeLit)
	if !ok || !ident(literal.Type, typeName) {
		return nil, false
	}
	return literal, true
}

func keyedValue(literal *ast.CompositeLit, index int, key string) (ast.Expr, bool) {
	if literal == nil || index < 0 || index >= len(literal.Elts) {
		return nil, false
	}
	pair, ok := literal.Elts[index].(*ast.KeyValueExpr)
	return pair.Value, ok && ident(pair.Key, key)
}

func exactStructuredWrapper(expr ast.Expr) bool {
	literal, ok := pointerComposite(expr, "appZaloStructuredAnswerRunner")
	if !ok || len(literal.Elts) != 14 {
		return false
	}
	direct := []struct {
		key, value string
	}{
		{"a", "a"}, {"run", "structured"}, {"zc", "effectiveConfig"},
		{"threadID", "threadID"}, {"question", "question"},
	}
	for index, want := range direct {
		value, ok := keyedValue(literal, index, want.key)
		if !ok || !ident(value, want.value) {
			return false
		}
	}
	current, ok := keyedValue(literal, 5, "currentZaloMsgID")
	currentCall, callOK := current.(*ast.CallExpr)
	if !ok || !callOK || !ident(currentCall.Fun, "appZaloCurrentMsgID") ||
		len(currentCall.Args) != 1 || !selector(currentCall.Args[0], "reply", "ReplyQuote") {
		return false
	}
	snapshotFields := []string{
		"history", "directFiles", "files", "highWater", "highWaterKnown", "resumeDelta",
	}
	for offset, field := range snapshotFields {
		value, ok := keyedValue(literal, 6+offset, field)
		if !ok || !selector(value, "snapshot", field) {
			return false
		}
	}
	binding, ok := keyedValue(literal, 12, "claudeBinding")
	if !ok || !ident(binding, "binding") {
		return false
	}
	displayName, ok := keyedValue(literal, 13, "agentDisplayName")
	return ok && selector(displayName, "snapshot", "agentDisplayName")
}

func exactCapturedWrapper(expr ast.Expr) bool {
	literal, ok := pointerComposite(expr, "appZaloCapturedIdentityRunner")
	if !ok || len(literal.Elts) != 2 {
		return false
	}
	runner, runnerOK := keyedValue(literal, 0, "zaloRunner")
	displayName, displayOK := keyedValue(literal, 1, "agentDisplayName")
	return runnerOK && ident(runner, "run") && displayOK &&
		selector(displayName, "snapshot", "agentDisplayName")
}

func structuredRouteAssignments(stmt *ast.IfStmt) (token.Pos, token.Pos, bool) {
	if !typedRunBinding(stmt, "structured", "appZaloStructuredRunner") ||
		len(stmt.Body.List) != 1 {
		return token.NoPos, token.NoPos, false
	}
	otherwise, ok := stmt.Else.(*ast.BlockStmt)
	if !ok || len(otherwise.List) != 1 {
		return token.NoPos, token.NoPos, false
	}
	selected, selectedOK := stmt.Body.List[0].(*ast.AssignStmt)
	fallback, fallbackOK := otherwise.List[0].(*ast.AssignStmt)
	selectedRight, selectedRightOK := exactAssignment(selected, "run")
	fallbackRight, fallbackRightOK := exactAssignment(fallback, "run")
	if !selectedOK || !fallbackOK || !selectedRightOK || !fallbackRightOK ||
		!exactStructuredWrapper(selectedRight) || !exactCapturedWrapper(fallbackRight) {
		return token.NoPos, token.NoPos, false
	}
	return selected.Pos(), fallback.Pos(), true
}

type routeTransforms struct {
	allowed                                      map[token.Pos]bool
	resolved, attachments, structured, wrappers int
	resolvePos, attachmentPos, structuredPos     token.Pos
	wrapperPos                                   token.Pos
}

func allowedRunAssignments(body *ast.BlockStmt) routeTransforms {
	result := routeTransforms{allowed: make(map[token.Pos]bool)}
	for _, statement := range body.List {
		switch stmt := statement.(type) {
		case *ast.AssignStmt:
			if resolvedRouteAssignment(stmt) {
				result.allowed[stmt.Pos()] = true
				result.resolved++
				result.resolvePos = stmt.Pos()
			}
		case *ast.IfStmt:
			if position, ok := awareRouteAssignment(
				stmt, "appZaloAttachmentAwareRunner", "appWithZaloAttachments"); ok {
				result.allowed[position] = true
				result.attachments++
				result.attachmentPos = stmt.Pos()
			}
			if position, ok := awareRouteAssignment(
				stmt, "appZaloStructuredClaudeAwareRunner", "appWithZaloStructuredClaudeRequirement"); ok {
				result.allowed[position] = true
				result.structured++
				result.structuredPos = stmt.Pos()
			}
			if selected, fallback, ok := structuredRouteAssignments(stmt); ok {
				result.allowed[selected] = true
				result.allowed[fallback] = true
				result.wrappers++
				result.wrapperPos = stmt.Pos()
			}
		}
	}
	return result
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
			if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "appZaloRunner" {
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
			if productionRuntimeRunner(arg) {
				participatingRunnerArgs++
			}
		}
		return true
	})
	directAnswerFactoryCalls := 0
	directAnswerFactoryRunnerArgs := 0
	if len(answer.Body.List) == 1 {
		if call, ok := directAnswerFactoryCall(answer.Body.List[0]); ok {
			directAnswerFactoryCalls++
			if len(call.Args) > 0 && productionRuntimeRunner(call.Args[len(call.Args)-1]) {
				directAnswerFactoryRunnerArgs++
			}
		}
	}
	if answerFactoryCalls != 1 || participatingRunnerArgs != 1 || totalRunnerSelectors != 1 ||
		directAnswerFactoryCalls != 1 || directAnswerFactoryRunnerArgs != 1 {
		fail("expected appAnswerZalo to be one direct factory call with the exact production appZaloRunner argument (calls=%d, arguments=%d, total selectors=%d, direct calls=%d, direct arguments=%d)",
			answerFactoryCalls, participatingRunnerArgs, totalRunnerSelectors,
			directAnswerFactoryCalls, directAnswerFactoryRunnerArgs)
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
	releaseCalls := 0
	answerExecutionCalls := 0
	var acquirePosition token.Pos
	factoryRouteAssignments := 0
	var routePosition token.Pos
	ast.Inspect(factory.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && selector(call.Fun, "appZaloProcessThreadGate", "Acquire") {
			acquireCalls++
			acquirePosition = call.Pos()
		}
		if call, ok := node.(*ast.CallExpr); ok && ident(call.Fun, "release") {
			releaseCalls++
		}
		if call, ok := node.(*ast.CallExpr); ok && selector(call.Fun, "a", "answerZalo") {
			answerExecutionCalls++
		}
		if position, ok := routeAssignment(node); ok {
			factoryRouteAssignments++
			routePosition = position
		}
		return true
	})
	directAcquireCalls := 0
	directDeferredReleases := 0
	directRouteNilGuards := 0
	directRouteAssignments := 0
	directAnswerExecutions := 0
	directAnswerRunArgs := 0
	effectiveConfigSources := 0
	turnSnapshotSources := 0
	var releasePosition token.Pos
	var answerPosition token.Pos
	var effectiveConfigPosition token.Pos
	var turnSnapshotPosition token.Pos
	for _, statement := range factory.Body.List {
		if routeNilGuard(statement) {
			directRouteNilGuards++
		}
		if position, ok := directAcquire(statement); ok {
			directAcquireCalls++
			acquirePosition = position
		}
		if position, ok := routeAssignment(statement); ok {
			directRouteAssignments++
			routePosition = position
		}
		if assignment, ok := statement.(*ast.AssignStmt); ok {
			if effectiveConfigSource(assignment) {
				effectiveConfigSources++
				effectiveConfigPosition = assignment.Pos()
			}
			if turnSnapshotSource(assignment) {
				turnSnapshotSources++
				turnSnapshotPosition = assignment.Pos()
			}
		}
		if position, ok := deferredRelease(statement); ok {
			directDeferredReleases++
			releasePosition = position
		}
		if call, ok := directAnswerExecution(statement); ok {
			directAnswerExecutions++
			answerPosition = call.Pos()
			if len(call.Args) > 2 && ident(call.Args[2], "run") {
				directAnswerRunArgs++
			}
		}
	}
	transforms := allowedRunAssignments(factory.Body)
	unauthorizedRunWrites := 0
	unauthorizedRouteWrites := 0
	valueRunDefinitions := 0
	valueRouteDefinitions := 0
	unauthorizedEffectiveConfigWrites := 0
	unauthorizedSnapshotWrites := 0
	releaseIdentifiers := 0
	routeIdentifiers := 0
	protectedAddressTakes := 0
	ast.Inspect(factory.Body, func(node ast.Node) bool {
		if assignment, ok := node.(*ast.AssignStmt); ok {
			if assignmentWrites(assignment, "run") {
				if _, route := routeAssignment(assignment); !route &&
					(!transforms.allowed[assignment.Pos()] || assignment.Pos() <= routePosition ||
						assignment.Pos() >= answerPosition) {
					unauthorizedRunWrites++
				}
			}
			if assignmentWrites(assignment, "route") {
				unauthorizedRouteWrites++
			}
			if assignmentWritesRoot(assignment, "effectiveConfig") &&
				!effectiveConfigSource(assignment) && !resolvedRouteAssignment(assignment) {
				unauthorizedEffectiveConfigWrites++
			}
			if assignmentWritesRoot(assignment, "snapshot") && !turnSnapshotSource(assignment) {
				unauthorizedSnapshotWrites++
			}
		}
		if spec, ok := node.(*ast.ValueSpec); ok {
			for _, name := range spec.Names {
				if name.Name == "run" {
					valueRunDefinitions++
				}
				if name.Name == "route" {
					valueRouteDefinitions++
				}
			}
		}
		if name, ok := node.(*ast.Ident); ok {
			switch name.Name {
			case "release":
				releaseIdentifiers++
			case "route":
				routeIdentifiers++
			}
		}
		if address, ok := node.(*ast.UnaryExpr); ok && address.Op == token.AND {
			for _, protected := range []string{
				"release", "route", "run", "effectiveConfig", "snapshot",
			} {
				if expressionRootedAt(address.X, protected) {
					protectedAddressTakes++
					break
				}
			}
		}
		return true
	})
	if globalRouteAssignments != 1 || acquireCalls != 1 || releaseCalls != 1 ||
		factoryRouteAssignments != 1 || answerExecutionCalls != 1 || directRouteNilGuards != 1 ||
		directAcquireCalls != 1 ||
		directDeferredReleases != 1 || directRouteAssignments != 1 || directAnswerExecutions != 1 ||
		directAnswerRunArgs != 1 || acquirePosition >= releasePosition || releasePosition >= routePosition ||
		effectiveConfigSources != 1 || turnSnapshotSources != 1 ||
		transforms.resolved != 1 || transforms.attachments != 1 || transforms.structured != 1 ||
		transforms.wrappers != 1 || routePosition >= effectiveConfigPosition ||
		effectiveConfigPosition >= transforms.resolvePos || transforms.resolvePos >= turnSnapshotPosition ||
		turnSnapshotPosition >= transforms.attachmentPos ||
		transforms.attachmentPos >= transforms.structuredPos ||
		transforms.structuredPos >= transforms.wrapperPos || transforms.wrapperPos >= answerPosition ||
		unauthorizedRunWrites != 0 || unauthorizedRouteWrites != 0 ||
		unauthorizedEffectiveConfigWrites != 0 || unauthorizedSnapshotWrites != 0 ||
		valueRunDefinitions != 0 || valueRouteDefinitions != 0 || releaseIdentifiers != 2 ||
		routeIdentifiers != 2 || protectedAddressTakes != 0 {
		fail("expected the exact top-level Provider hook and answer dataflow inside one deferred thread-gate lifetime (global routes=%d, acquires=%d, releases=%d, factory routes=%d, answer calls=%d, route nil guards=%d, direct acquires=%d, deferred releases=%d, direct routes=%d, direct answers=%d, direct run arguments=%d, effective config sources=%d, turn snapshot sources=%d, resolves=%d, attachment transforms=%d, structured transforms=%d, wrappers=%d, unauthorized run writes=%d, unauthorized route writes=%d, unauthorized config writes=%d, unauthorized snapshot writes=%d, run declarations=%d, route declarations=%d, release references=%d, route references=%d, protected address escapes=%d)",
			globalRouteAssignments, acquireCalls, releaseCalls, factoryRouteAssignments,
			answerExecutionCalls, directRouteNilGuards, directAcquireCalls,
			directDeferredReleases, directRouteAssignments,
			directAnswerExecutions, directAnswerRunArgs, effectiveConfigSources,
			turnSnapshotSources, transforms.resolved,
			transforms.attachments, transforms.structured, transforms.wrappers, unauthorizedRunWrites,
			unauthorizedRouteWrites, unauthorizedEffectiveConfigWrites, unauthorizedSnapshotWrites,
			valueRunDefinitions, valueRouteDefinitions,
			releaseIdentifiers, routeIdentifiers, protectedAddressTakes)
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

function Get-AppSafePackageEntries {
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
      Write-Output $child
      if ($child.PSIsContainer) {
        $pending.Push($child.FullName)
      }
    }
  }
}

function Get-AppSafePackageFiles {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Root)

  Get-AppSafePackageEntries -Root $Root | Where-Object { -not $_.PSIsContainer }
}

function Assert-AppPackageSensitiveContentAbsent {
  param(
    [Parameter(Mandatory)][string]$Out,
    [IO.FileInfo[]]$Files
  )

  # Test canaries stand in for prompt, response, stderr, and credential data.
  # Scan bytes rather than decoded text so binaries and the encodings below are
  # covered with a fixed 64 KiB working buffer.
  $sensitiveMarkers = @(
    'APP_TEST_',
    'ONBOARDING_TEST_TOKEN_CANARY_CLEAR_4F91',
    'ONBOARDING_USER_PROMPT_CANARY_CLEAR_8172',
    'ONBOARDING_ANSWER_CANARY_CLEAR_BC43',
    'ONBOARDING_CREDENTIAL_CANARY_CLEAR_D912',
    'ACCOUNT_CONFIG_DIR_CANARY_CLEAR_A19E'
  )
  $needles = [Collections.Generic.List[byte[]]]::new()
  foreach ($encoding in @(
      [Text.UTF8Encoding]::new($false),
      [Text.UnicodeEncoding]::new($false, $false),
      [Text.UnicodeEncoding]::new($true, $false)
    )) {
    foreach ($marker in $sensitiveMarkers) {
      $needles.Add($encoding.GetBytes($marker))
    }
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

function Assert-AppPackageOnboarding {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Path)

  try {
    $readme = [IO.File]::ReadAllText([IO.Path]::GetFullPath($Path))
  } catch {
    throw 'package README could not be read'
  }

  foreach ($staleSignature in @(
      'Sau khoang 4 giay trinh duyet tu mo trang Zalo.',
      'Day la buoc DUY NHAT khong the bo',
      'Neu chon Claude Code, co the cai CLI tai',
      'Cai Claude Code global thu cong truoc khi mo Portal.',
      'Voi Provider API, nhap endpoint,',
      'Voi Provider API, nhap endpoint, API key va model trong Portal.',
      'Mo trang Combos va kich hoat Combo truoc khi ket noi Zalo.',
      'Mo persona.md va thay {{TEN_BOT}} truoc khi chay.'
    )) {
    if ($readme.IndexOf($staleSignature, [StringComparison]::Ordinal) -ge 0) {
      throw 'package README contains misleading setup guidance'
    }
  }
  $normalizedReadme = [regex]::Replace($readme, '\s+', ' ')

  $required = [ordered]@{
    'Start.vbs mo Portal quan ly tai http://127.0.0.1:8770/.' = 'management Portal startup guidance'
    '1. Chon Provider' = 'Provider selection guidance'
    'Bat ON roi bam Connect hoac Bam de cai tren dung dong do' = 'per-row Provider install guidance'
    '2. Khi Provider cuoi cung san sang' = 'automatic bootstrap start guidance'
    'Portal tu hien "Dang chuan bi tro ly" va tu hoan tat' = 'automatic bootstrap progress guidance'
    'Khong co buoc Persona, Test Chat hay Complete de bam' = 'no-manual-wizard guidance'
    '3. Khi Portal bao "San sang"' = 'ready guidance'
    'vao Portal de quan ly hoac mo trang Zalo' = 'Portal and Zalo guidance'
    'trang Knowledge la tuy chon' = 'optional Knowledge guidance'
    'tri thuc trong do khong chan Done' = 'Knowledge-does-not-block guidance'
    'he thong khong tao route; bot co y im lang' = 'failed-setup no-route silence guidance'
  }
  foreach ($entry in $required.GetEnumerator()) {
    if ($normalizedReadme.IndexOf($entry.Key, [StringComparison]::Ordinal) -lt 0) {
      throw "package README missing $($entry.Value)"
    }
  }

  $previous = -1
  foreach ($step in @(
      'Start.vbs mo Portal quan ly tai http://127.0.0.1:8770/.',
      '1. Chon Provider',
      '2. Khi Provider cuoi cung san sang',
      '3. Khi Portal bao "San sang"'
    )) {
    $current = $normalizedReadme.IndexOf($step, [StringComparison]::Ordinal)
    if ($current -le $previous) {
      throw 'package README onboarding order must be Start, Provider, automatic bootstrap, then Portal/Zalo'
    }
    $previous = $current
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
    'assets\components\provider-connect.js'
    'assets\components\persona-fields.js'
    'assets\pages\onboarding.js'
    'assets\pages\settings.js'
  )
  foreach ($relative in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $outPath $relative) -PathType Leaf)) {
      throw "package missing $relative"
    }
  }
  Assert-AppPackageOnboarding -Path (Join-Path $outPath 'README.txt')

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
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature 'claude_account_id' -Label 'Zalo session Claude account binding signature'
  Assert-AppPackageBinaryContains -Path $binary `
    -Signature 'claude_config_dir' -Label 'Zalo session Claude config binding signature'
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

  $credentials = Join-Path $outPath 'data\zalo\credentials.json'
  if (-not $AllowZaloCredentials -and (Test-Path -LiteralPath $credentials -PathType Leaf)) {
    throw 'package contains Zalo credentials'
  }
  $packageFiles = @(Get-AppSafePackageFiles -Root $outPath)
  Assert-AppPackageSensitiveContentAbsent -Out $outPath -Files $packageFiles
  Assert-AppProviderCredentialFilesAbsent -Package $outPath `
    -Canary $script:ProviderCredentialCanary -Files $packageFiles

  return $outPath
}

Export-ModuleMember -Function Resolve-BuildPaths, Assert-CleanGitSource, Get-AppGoTestSkipPattern, Clear-AppOutput, Resolve-PersonaSource, Read-AppPersonaSourceSnapshot, Write-AppPersonaPackage, Assert-AppPersonaPackagePrivacy, New-AppStage, Apply-AppSeams, Assert-AppProviderRouteTopology, Assert-NoProviderCredential, Assert-AppPackage
