# Errors

Command failures and integration errors.

---

## [ERR-20260720-002] ripgrep_windows_path_glob

**Logged**: 2026-07-20T13:05:00+08:00
**Priority**: low
**Status**: resolved
**Area**: config

### Summary
Ripgrep on Windows rejected Unix-style `directory/*.go` path arguments.

### Error
```
文件名、目录名或卷标语法不正确。 (os error 123)
```

### Context
- Operation: enumerate declarations across several Go packages
- Environment: PowerShell on Windows
- The grouped parallel call discarded otherwise successful reads

### Suggested Fix
Pass package directories as path arguments and use `-g '*.go'` for file filtering.

### Metadata
- Reproducible: yes
- Related Files: none
- See Also: ERR-20260720-001

### Resolution
- **Resolved**: 2026-07-20T13:06:00+08:00
- **Notes**: Command form corrected to use ripgrep's glob option.

---

## [ERR-20260720-003] baseline_toolchain_missing_from_path

**Logged**: 2026-07-20T13:15:00+08:00
**Priority**: medium
**Status**: in_progress
**Area**: infra

### Summary
The baseline test command could not start because Go is not available on the system PATH.

### Error
```
go : The term 'go' is not recognized as the name of a cmdlet...
```

### Context
- Operation: parallel `go test`, `go vet`, frontend checks, and script syntax checks
- Environment: PowerShell on Windows
- The grouped call stopped before returning independent frontend/script results

### Suggested Fix
Use the repository's `.tools` toolchain when present, and run each check independently so an unavailable compiler does not mask other diagnostics.

### Metadata
- Reproducible: yes
- Related Files: `.tools`, `package.bat`

---

## [ERR-20260720-001] parallel_repository_inventory

**Logged**: 2026-07-20T13:00:00+08:00
**Priority**: low
**Status**: resolved
**Area**: config

### Summary
A parallel repository inventory lost all output because one expected no-match search exited with status 1.

### Error
```
Script failed because `rg` returned its normal no-match exit status inside a grouped call.
```

### Context
- Operation: parallel root listing, instruction-file search, Git status, and file inventory
- Environment: PowerShell on Windows
- No project command or source code failed

### Suggested Fix
Normalize expected `rg` no-match status to success, or run searches independently when partial output matters.

### Metadata
- Reproducible: yes
- Related Files: none

### Resolution
- **Resolved**: 2026-07-20T13:01:00+08:00
- **Notes**: Re-ran with explicit handling for exit status 1 and received the full inventory.

---
